package cli

import (
	"encoding/json"
	"io"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// printControllerStructured returns false when the command should render its
// human-readable view. JSON and YAML share the controller's protobuf JSON shape.
func printControllerStructured(out io.Writer, response proto.Message) (bool, error) {
	if outputFormat != "json" && outputFormat != "yaml" {
		return false, nil
	}
	return true, printControllerMessage(out, response, outputFormat)
}

func printControllerMessage(out io.Writer, response proto.Message, format string) error {
	data, err := (protojson.MarshalOptions{Indent: "  "}).Marshal(response)
	if err != nil {
		return err
	}
	if format == "yaml" {
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		data, err = yaml.Marshal(value)
		if err != nil {
			return err
		}
	}
	if _, err := out.Write(data); err != nil {
		return err
	}
	_, err = io.WriteString(out, "\n")
	return err
}
