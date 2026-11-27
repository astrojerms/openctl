// Package apps manages standalone applications on explicitly selected SSH guests.
package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/openctl/openctl/pkg/protocol"
)

const APIVersion = "apps.openctl.io/v1"

// StateStore is satisfied by providerstate.Store. No resolved manifest is saved.
type StateStore interface {
	LoadState(context.Context, string, string, string) ([]byte, []byte, int, error)
	SaveState(context.Context, string, string, string, []byte, []byte, int) error
	DeleteState(context.Context, string, string, string) error
}

// NameLister is satisfied by manifests.Store.
type NameLister interface {
	ListNames(context.Context, string, string) (map[string]bool, error)
}

type Target struct {
	Host           string `json:"host"`
	Port           int    `json:"port,omitempty"`
	User           string `json:"user"`
	PrivateKeyFile string `json:"privateKeyFile"`
	KnownHostsFile string `json:"knownHostsFile,omitempty"`
	Sudo           bool   `json:"sudo,omitempty"`
}

type probe struct {
	URL string `json:"url"`
}
type account struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type nasSpec struct {
	Server      string `json:"server"`
	Share       string `json:"share"`
	MediaPath   string `json:"mediaPath"`
	Credentials string `json:"credentials"`
}
type spec struct {
	Target            Target  `json:"target"`
	Project           string  `json:"project"`
	Directory         string  `json:"directory"`
	Compose           string  `json:"compose"`
	Environment       string  `json:"environment"`
	Probe             probe   `json:"probe"`
	PublicEndpoint    string  `json:"publicEndpoint"`
	Image             string  `json:"image"`
	Bootstrap         bool    `json:"bootstrap"`
	AllowRemoteAccess bool    `json:"allowRemoteAccess"`
	NAS               nasSpec `json:"nas"`
	Admin             account `json:"admin"`
	Viewer            account `json:"viewer"`
}

// locator deliberately excludes Compose, environment, passwords and NAS credentials.
type locator struct {
	Target         Target `json:"target"`
	Project        string `json:"project"`
	Directory      string `json:"directory"`
	Probe          probe  `json:"probe"`
	PublicEndpoint string `json:"publicEndpoint,omitempty"`
	Image          string `json:"image,omitempty"`
	NASServer      string `json:"nasServer,omitempty"`
	NASShare       string `json:"nasShare,omitempty"`
	NASMediaPath   string `json:"nasMediaPath,omitempty"`
}

var safeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
var nasName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
var officialImage = regexp.MustCompile(`^(docker\.io/)?jellyfin/jellyfin@sha256:[a-f0-9]{64}$`)

func checkKind(kind string) error {
	if kind != "ComposeApplication" && kind != "Jellyfin" {
		return fmt.Errorf("apps: unsupported kind %q", kind)
	}
	return nil
}

func parse(r *protocol.Resource, resolved bool) (spec, locator, error) {
	var s spec
	var l locator
	if r == nil {
		return s, l, fmt.Errorf("apps: resource is required")
	}
	if err := checkKind(r.Kind); err != nil {
		return s, l, err
	}
	if r.APIVersion != APIVersion || !safeName.MatchString(r.Metadata.Name) {
		return s, l, fmt.Errorf("apps: valid apiVersion and resource name required")
	}
	// Preview receives unresolved markers. Only decode the locator there.
	source := r.Spec
	if !resolved {
		source = make(map[string]any, len(r.Spec))
		for k, v := range r.Spec {
			if k != "compose" && k != "environment" && k != "admin" && k != "viewer" && k != "nas" {
				source[k] = v
			}
		}
		if n, ok := r.Spec["nas"].(map[string]any); ok {
			source["nas"] = map[string]any{"server": n["server"], "share": n["share"], "mediaPath": n["mediaPath"]}
		}
	}
	b, err := json.Marshal(source)
	if err != nil {
		return s, l, fmt.Errorf("apps: invalid spec")
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, l, fmt.Errorf("apps: invalid spec or unresolved reference/secret; resolve target and credentials before apply")
	}
	if s.Target.Port == 0 {
		s.Target.Port = 22
	}
	if s.Target.KnownHostsFile == "" {
		s.Target.KnownHostsFile = "~/.ssh/known_hosts"
	}
	if s.Target.Host == "" || strings.ContainsAny(s.Target.Host, "\r\n\x00 /\\") || s.Target.User == "" || s.Target.PrivateKeyFile == "" || s.Target.Port < 1 || s.Target.Port > 65535 {
		return s, l, fmt.Errorf("apps: target requires host, user, privateKeyFile and a valid port")
	}
	if s.Project == "" {
		s.Project = "openctl-" + r.Metadata.Name
	}
	if s.Directory == "" {
		s.Directory = "/opt/openctl/apps/" + r.Metadata.Name
	}
	if r.Kind == "Jellyfin" {
		if (s.Project != "openctl-"+r.Metadata.Name && s.Project != "jellyfin") || (s.Directory != "/opt/openctl/apps/"+r.Metadata.Name && s.Directory != "/opt/jellyfin") {
			return s, l, fmt.Errorf("apps: Jellyfin uses fixed project jellyfin and directory /opt/jellyfin")
		}
		s.Project, s.Directory = "jellyfin", "/opt/jellyfin"
		s.Probe.URL = "http://127.0.0.1:8096/health"
		if !officialImage.MatchString(s.Image) {
			return s, l, fmt.Errorf("apps: Jellyfin image must be the official jellyfin/jellyfin repository pinned by sha256 digest")
		}
		if !nasName.MatchString(s.NAS.Server) || !nasName.MatchString(s.NAS.Share) {
			return s, l, fmt.Errorf("apps: nas.server and nas.share must be simple DNS/share names")
		}
		if !validMediaPath(s.NAS.MediaPath) {
			return s, l, fmt.Errorf("apps: nas.mediaPath must be empty (share root) or a clean relative subdirectory without traversal, backslashes or control characters")
		}
		if resolved && (s.NAS.Credentials == "" || s.Admin.Username == "" || s.Admin.Password == "" || s.Viewer.Username == "" || s.Viewer.Password == "" || s.Admin.Username == s.Viewer.Username) {
			return s, l, fmt.Errorf("apps: distinct admin/viewer accounts and resolved NAS/admin/viewer secrets are required")
		}
	} else if resolved && strings.TrimSpace(s.Compose) == "" {
		return s, l, fmt.Errorf("apps: compose document is required")
	}
	if !safeName.MatchString(s.Project) || !path.IsAbs(s.Directory) || path.Clean(s.Directory) != s.Directory || s.Directory == "/" || strings.ContainsAny(s.Directory, "\r\n\x00") {
		return s, l, fmt.Errorf("apps: project must be a safe Compose name and directory a clean absolute non-root path")
	}
	if err := validateURL(s.Probe.URL); err != nil {
		return s, l, fmt.Errorf("apps: probe.url must be an HTTP(S) URL without credentials")
	}
	if s.PublicEndpoint != "" {
		if err := validateURL(s.PublicEndpoint); err != nil {
			return s, l, fmt.Errorf("apps: publicEndpoint must be an HTTP(S) URL without credentials")
		}
	}
	l = locator{Target: s.Target, Project: s.Project, Directory: s.Directory, Probe: s.Probe, PublicEndpoint: s.PublicEndpoint, Image: s.Image, NASServer: s.NAS.Server, NASShare: s.NAS.Share, NASMediaPath: s.NAS.MediaPath}
	return s, l, nil
}

func validateURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid URL")
	}
	return nil
}

func placementChange(old, next locator) error {
	if old.Target.Host != next.Target.Host || old.Target.Port != next.Target.Port || old.Project != next.Project || old.Directory != next.Directory || old.NASServer != next.NASServer || old.NASShare != next.NASShare || old.NASMediaPath != next.NASMediaPath {
		return fmt.Errorf("apps: target host/port, project, directory and NAS placement are immutable: changing them would orphan the prior application or switch its media; create a separately named application and explicitly retire the old one")
	}
	return nil
}

func validMediaPath(value string) bool {
	if value == "" {
		return true
	}
	if path.IsAbs(value) || path.Clean(value) != value || strings.Contains(value, "\\") || strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
