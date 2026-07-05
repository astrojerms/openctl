package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func newWhoAmICommand() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the identity and RBAC role of your controller credential",
		Long: `Calls the controller's WhoAmI RPC and prints who your token authenticates
as and its RBAC role (viewer, editor, or admin).

Useful for confirming which credential ~/.openctl/config.yaml is using and
what it is allowed to do. The root token reports as the admin root credential;
named-user tokens and browser sessions report their own user and role.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWhoAmI(cmd.Context())
		},
	}
}

func runWhoAmI(ctx context.Context) error {
	conn, token, err := dialController()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	client := apiv1.NewSessionServiceClient(conn)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

	resp, err := client.WhoAmI(ctx, &apiv1.WhoAmIRequest{})
	if err != nil {
		return fmt.Errorf("whoami: %w", err)
	}
	fmt.Println(formatWhoAmI(resp))
	return nil
}

// formatWhoAmI renders a WhoAmI response as a single human-readable line.
// Pure so it can be unit-tested without a controller.
func formatWhoAmI(resp *apiv1.WhoAmIResponse) string {
	user := resp.GetUserId()
	if user == "" {
		// Empty user_id is the "root credential, not a session" signal.
		user = "<root>"
	}
	role := resp.GetRole()
	if role == "" {
		role = "unknown"
	}
	line := fmt.Sprintf("user: %s  role: %s", user, role)
	if sid := resp.GetSessionId(); sid != "" {
		line += "  session: " + sid
	}
	return line
}
