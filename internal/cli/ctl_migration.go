package cli

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/internal/manifest"
	"github.com/openctl/openctl/internal/schema"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func newCtlExportCommand() *cobra.Command {
	var file, apiVersion string
	cmd := &cobra.Command{
		Use:   "export <kind> <name> -f <bundle.json>",
		Short: "Export ownership evidence without releasing the resource",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" || strings.TrimSpace(apiVersion) == "" {
				return fmt.Errorf("--file and --api-version are required")
			}
			// Reserve the destination before contacting the controller. Never truncate
			// an existing bundle, and remove only the file this invocation created.
			return writeMigrationExport(file, func() (*apiv1.MigrationBundle, error) {
				conn, token, err := dialController()
				if err != nil {
					return nil, err
				}
				defer func() { _ = conn.Close() }()
				ctx := metadata.AppendToOutgoingContext(cmd.Context(), "authorization", "Bearer "+token)
				resp, err := apiv1.NewResourceServiceClient(conn).ExportResource(ctx, &apiv1.ExportResourceRequest{ApiVersion: apiVersion, Kind: args[0], Name: args[1]})
				if err != nil {
					return nil, fmt.Errorf("controller export failed (%s): %s", status.Code(err), status.Convert(err).Message())
				}
				return resp.GetBundle(), nil
			}, func() error {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "Resource exported; source ownership retained.")
				return err
			})
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "new destination bundle file (mode 0600; never overwritten)")
	cmd.Flags().StringVar(&apiVersion, "api-version", "", "resource apiVersion")
	return cmd
}

func newCtlAdoptCommand() *cobra.Command {
	var file, providerContext string
	cmd := &cobra.Command{
		Use:   "adopt -f <bundle.json|manifest.yaml|manifest.cue>",
		Short: "Adopt an existing VM without changing it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			req, err := loadMigrationAdoption(file)
			if err != nil {
				return err
			}
			req.ProviderContext = providerContext
			conn, token, err := dialController()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			ctx := metadata.AppendToOutgoingContext(cmd.Context(), "authorization", "Bearer "+token)
			if _, err := apiv1.NewResourceServiceClient(conn).AdoptResource(ctx, req); err != nil {
				return fmt.Errorf("controller adoption failed (%s): %s", status.Code(err), status.Convert(err).Message())
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Resource adopted without modifying the VM.")
			return err
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "migration bundle or single YAML/CUE manifest")
	cmd.Flags().StringVar(&providerContext, "provider-context", "", "destination provider context (original bundle remains unchanged)")
	return cmd
}

func newCtlForgetCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "forget -f <source-bundle.json>",
		Short: "Release controller ownership without deleting the VM",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			bundle, err := loadMigrationBundle(file)
			if err != nil {
				return err
			}
			conn, token, err := dialController()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			ctx := metadata.AppendToOutgoingContext(cmd.Context(), "authorization", "Bearer "+token)
			if _, err := apiv1.NewResourceServiceClient(conn).ForgetResource(ctx, &apiv1.ForgetResourceRequest{Bundle: bundle}); err != nil {
				return fmt.Errorf("controller forget failed (%s): %s", status.Code(err), status.Convert(err).Message())
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Resource ownership released; VM retained.")
			return err
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "original source migration bundle")
	return cmd
}

func validateMigrationBundle(bundle *apiv1.MigrationBundle) error {
	if bundle == nil || bundle.Version != 1 {
		return fmt.Errorf("unsupported or missing migration bundle version (expected 1)")
	}
	r := bundle.Resource
	if r == nil || strings.TrimSpace(r.ApiVersion) == "" || strings.TrimSpace(r.Kind) == "" || r.Metadata == nil || strings.TrimSpace(r.Metadata.Name) == "" || r.Spec == nil || len(r.Spec.Fields) == 0 || r.Status != nil {
		return fmt.Errorf("migration bundle requires a complete desired resource without status")
	}
	digest, err := hex.DecodeString(bundle.ManifestDigest)
	if strings.TrimSpace(bundle.ControllerId) == "" || strings.TrimSpace(bundle.Identity) == "" || err != nil || len(digest) != 32 {
		return fmt.Errorf("migration bundle requires controller ID, identity, and SHA-256 manifest digest")
	}
	return nil
}

func loadMigrationBundle(path string) (*apiv1.MigrationBundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read migration bundle: %w", err)
	}
	bundle := new(apiv1.MigrationBundle)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(data, bundle); err != nil {
		// Decoder errors can contain confidential manifest values.
		return nil, fmt.Errorf("invalid migration bundle JSON (unknown fields or malformed values)")
	}
	if err := validateMigrationBundle(bundle); err != nil {
		return nil, err
	}
	return bundle, nil
}

func loadMigrationAdoption(path string) (*apiv1.AdoptResourceRequest, error) {
	if strings.EqualFold(filepath.Ext(path), ".json") {
		bundle, err := loadMigrationBundle(path)
		if err != nil {
			return nil, err
		}
		return &apiv1.AdoptResourceRequest{Bundle: bundle}, nil
	}
	if filepath.Ext(path) == ".cue" {
		r, err := loadManifest(path)
		if err != nil {
			return nil, fmt.Errorf("invalid single-resource CUE manifest")
		}
		if err := schema.Validate(r); err != nil {
			return nil, fmt.Errorf("invalid adoption manifest schema")
		}
		wire, err := resourceToProto(r)
		if err != nil {
			return nil, fmt.Errorf("invalid adoption manifest values")
		}
		wire.Status = nil
		return &apiv1.AdoptResourceRequest{Resource: wire}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read adoption manifest: %w", err)
	}
	// The shared single-document parser ignores trailing YAML documents. Check
	// the stream first so adoption cannot silently choose one of several VMs.
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("invalid single-resource YAML manifest")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("adoption requires exactly one YAML document")
	}
	r, err := manifest.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("invalid adoption manifest")
	}
	if err := schema.Validate(r); err != nil {
		return nil, fmt.Errorf("invalid adoption manifest schema")
	}
	wire, err := resourceToProto(r)
	if err != nil {
		return nil, fmt.Errorf("invalid adoption manifest values")
	}
	wire.Status = nil
	return &apiv1.AdoptResourceRequest{Resource: wire}, nil
}

func writeMigrationExport(path string, export func() (*apiv1.MigrationBundle, error), completed func() error) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create export file: %w", err)
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	bundle, err := export()
	if err != nil {
		return err
	}
	if err := validateMigrationBundle(bundle); err != nil {
		return err
	}
	data, err := (protojson.MarshalOptions{Indent: "  "}).Marshal(bundle)
	if err != nil {
		return fmt.Errorf("cannot encode migration bundle")
	}
	data = append(data, '\n')
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("cannot write export file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("cannot sync export file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cannot close export file: %w", err)
	}
	committed = true
	return completed()
}
