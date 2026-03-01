package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/pkg/protocol"
)

// Format represents an output format
type Format string

const (
	FormatTable Format = "table"
	FormatYAML  Format = "yaml"
	FormatJSON  Format = "json"
	FormatWide  Format = "wide"
)

// Formatter handles output formatting
type Formatter struct {
	format Format
	writer io.Writer
}

// NewFormatter creates a new formatter
func NewFormatter(format Format, writer io.Writer) *Formatter {
	return &Formatter{
		format: format,
		writer: writer,
	}
}

// FormatResources formats a list of resources
func (f *Formatter) FormatResources(resources []*protocol.Resource) error {
	switch f.format {
	case FormatJSON:
		return f.formatJSON(resources)
	case FormatYAML:
		return f.formatYAML(resources)
	case FormatTable, FormatWide:
		return f.formatTable(resources)
	default:
		return f.formatTable(resources)
	}
}

// FormatResource formats a single resource
func (f *Formatter) FormatResource(resource *protocol.Resource) error {
	switch f.format {
	case FormatJSON:
		return f.formatJSONSingle(resource)
	case FormatYAML:
		return f.formatYAMLSingle(resource)
	case FormatTable, FormatWide:
		return f.formatTable([]*protocol.Resource{resource})
	default:
		return f.formatTable([]*protocol.Resource{resource})
	}
}

func (f *Formatter) formatJSON(resources []*protocol.Resource) error {
	encoder := json.NewEncoder(f.writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(resources)
}

func (f *Formatter) formatJSONSingle(resource *protocol.Resource) error {
	encoder := json.NewEncoder(f.writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(resource)
}

func (f *Formatter) formatYAML(resources []*protocol.Resource) error {
	for i, r := range resources {
		if i > 0 {
			fmt.Fprintln(f.writer, "---")
		}
		data, err := yaml.Marshal(r)
		if err != nil {
			return err
		}
		fmt.Fprint(f.writer, string(data))
	}
	return nil
}

func (f *Formatter) formatYAMLSingle(resource *protocol.Resource) error {
	data, err := yaml.Marshal(resource)
	if err != nil {
		return err
	}
	fmt.Fprint(f.writer, string(data))
	return nil
}

func (f *Formatter) formatTable(resources []*protocol.Resource) error {
	if len(resources) == 0 {
		fmt.Fprintln(f.writer, "No resources found")
		return nil
	}

	w := tabwriter.NewWriter(f.writer, 0, 0, 2, ' ', 0)
	defer w.Flush()

	kind := resources[0].Kind
	columns := getColumnsForKind(kind, f.format == FormatWide)

	headers := make([]string, len(columns))
	for i, col := range columns {
		headers[i] = strings.ToUpper(col.Header)
	}
	fmt.Fprintln(w, strings.Join(headers, "\t"))

	for _, r := range resources {
		values := make([]string, len(columns))
		for i, col := range columns {
			values[i] = col.Getter(r)
		}
		fmt.Fprintln(w, strings.Join(values, "\t"))
	}

	return nil
}

// Column represents a table column
type Column struct {
	Header string
	Getter func(*protocol.Resource) string
}

func getColumnsForKind(kind string, wide bool) []Column {
	columns := []Column{
		{Header: "NAME", Getter: func(r *protocol.Resource) string { return r.Metadata.Name }},
	}

	switch kind {
	case "VirtualMachine":
		columns = append(columns,
			Column{Header: "STATUS", Getter: func(r *protocol.Resource) string {
				if r.Status == nil {
					return "Unknown"
				}
				if status, ok := r.Status["state"].(string); ok {
					return status
				}
				return "Unknown"
			}},
			Column{Header: "CPU", Getter: func(r *protocol.Resource) string {
				if r.Spec == nil {
					return "-"
				}
				if cpu, ok := r.Spec["cpu"].(map[string]any); ok {
					if cores, ok := cpu["cores"]; ok {
						return fmt.Sprintf("%v", cores)
					}
				}
				return "-"
			}},
			Column{Header: "MEMORY", Getter: func(r *protocol.Resource) string {
				if r.Spec == nil {
					return "-"
				}
				if mem, ok := r.Spec["memory"].(map[string]any); ok {
					if size, ok := mem["size"]; ok {
						return fmt.Sprintf("%vMi", size)
					}
				}
				return "-"
			}},
		)
		if wide {
			columns = append(columns,
				Column{Header: "NODE", Getter: func(r *protocol.Resource) string {
					if r.Spec == nil {
						return "-"
					}
					if node, ok := r.Spec["node"].(string); ok {
						return node
					}
					return "-"
				}},
				Column{Header: "VMID", Getter: func(r *protocol.Resource) string {
					if r.Status == nil {
						return "-"
					}
					if vmid, ok := r.Status["vmid"]; ok {
						return fmt.Sprintf("%v", vmid)
					}
					return "-"
				}},
			)
		}
	case "Cluster":
		columns = append(columns,
			Column{Header: "PHASE", Getter: func(r *protocol.Resource) string {
				if r.Status == nil {
					return "Unknown"
				}
				if phase, ok := r.Status["phase"].(string); ok {
					return phase
				}
				return "Unknown"
			}},
			Column{Header: "NODES", Getter: func(r *protocol.Resource) string {
				if r.Spec == nil {
					return "-"
				}
				nodes, ok := r.Spec["nodes"].(map[string]any)
				if !ok {
					return "-"
				}
				count := 0
				if cp, ok := nodes["controlPlane"].(map[string]any); ok {
					if c, ok := cp["count"].(float64); ok {
						count += int(c)
					}
				}
				if workers, ok := nodes["workers"].([]any); ok {
					for _, w := range workers {
						if worker, ok := w.(map[string]any); ok {
							if c, ok := worker["count"].(float64); ok {
								count += int(c)
							}
						}
					}
				}
				return fmt.Sprintf("%d", count)
			}},
		)
		if wide {
			columns = append(columns,
				Column{Header: "PROVIDER", Getter: func(r *protocol.Resource) string {
					if r.Spec == nil {
						return "-"
					}
					if compute, ok := r.Spec["compute"].(map[string]any); ok {
						if provider, ok := compute["provider"].(string); ok {
							return provider
						}
					}
					return "-"
				}},
				Column{Header: "KUBECONFIG", Getter: func(r *protocol.Resource) string {
					if r.Status == nil {
						return "-"
					}
					if path, ok := r.Status["kubeconfigPath"].(string); ok {
						return path
					}
					return "-"
				}},
			)
		}
	default:
		if wide {
			columns = append(columns,
				Column{Header: "KIND", Getter: func(r *protocol.Resource) string { return r.Kind }},
				Column{Header: "APIVERSION", Getter: func(r *protocol.Resource) string { return r.APIVersion }},
			)
		}
	}

	return columns
}

// PrintMessage prints a simple message
func PrintMessage(w io.Writer, message string) {
	fmt.Fprintln(w, message)
}

// PrintError prints an error message
func PrintError(w io.Writer, err error) {
	fmt.Fprintf(w, "Error: %v\n", err)
}
