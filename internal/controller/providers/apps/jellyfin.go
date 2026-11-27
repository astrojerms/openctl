package apps

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// The managed recipe is embedded so controller rollout never depends on an
// operator staging scripts on the guest.
//
//go:embed assets/install.sh
var jellyfinInstaller string

//go:embed assets/compose.yaml
var jellyfinCompose string

//go:embed assets/onboard.py
var jellyfinOnboard string

func (p *Provider) applyJellyfin(ctx context.Context, l locator, s spec, identity string) error {
	environment := []byte("JELLYFIN_IMAGE=" + s.Image + "\nJELLYFIN_MEDIA_PATH='" + strings.ReplaceAll(mediaDirectory(l), "'", "\\'") + "'\n")
	// Never let retry of an interrupted initial onboarding restore a
	// host-networked container before credentials have been installed.
	// Established installations retain their config/cache and pinned image.
	installed, err := p.run(ctx, l, "inspect Jellyfin bootstrap marker", "if test -f /opt/jellyfin/.openctl-bootstrapped; then printf done; else printf missing; fi", nil)
	if err != nil {
		return err
	}
	if s.Bootstrap && strings.TrimSpace(installed) != "done" {
		if _, err := p.run(ctx, l, "prepare dedicated guest credentials directory", "set -eu; test \"$(id -u)\" = 0; install -d -m 0755 /etc/samba", nil); err != nil {
			return err
		}
		if err := p.upload(ctx, l, "/etc/samba/synology.credentials", []byte(s.NAS.Credentials)); err != nil {
			return err
		}
		if err := p.upload(ctx, l, "/opt/jellyfin/compose.yaml", []byte(jellyfinCompose)); err != nil {
			return err
		}
		if err := p.upload(ctx, l, "/opt/jellyfin/.env", environment); err != nil {
			return err
		}
		if err := p.upload(ctx, l, "/opt/jellyfin/install.sh", []byte(jellyfinInstaller)); err != nil {
			return err
		}
		_, installErr := p.run(ctx, l, "bootstrap dedicated Ubuntu 24.04 Docker/NVIDIA/CIFS guest", "NAS_SERVER="+quote(s.NAS.Server)+" NAS_SHARE="+quote(s.NAS.Share)+" bash /opt/jellyfin/install.sh && touch /opt/jellyfin/.openctl-bootstrapped", nil)
		if installErr != nil {
			if _, err := p.run(ctx, l, "check whether bootstrap requires reboot", "test -e /var/run/reboot-required", nil); err == nil {
				return rebootRequired()
			}
			return installErr
		}
	}
	reboot, err := p.run(ctx, l, "check guest reboot requirement", "if test -e /var/run/reboot-required; then printf required; else printf clear; fi", nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(reboot) == "required" {
		return rebootRequired()
	}
	// Refresh NAS credentials only via stdin; changing them never changes the
	// mount placement or rewrites application configuration/media.
	if err := p.upload(ctx, l, "/etc/samba/synology.credentials", []byte(s.NAS.Credentials)); err != nil {
		return err
	}
	if _, err := p.run(ctx, l, "verify Docker Compose, NVIDIA and the declared mounted read-only media", "set -eu; test \"$(id -u)\" = 0; docker compose version >/dev/null; command -v python3 >/dev/null; nvidia-smi >/dev/null; systemctl start mnt-synology.mount; ("+mediaCheck(l)+"); test -d /srv/jellyfin/config; test -d /srv/jellyfin/cache; test -f /etc/systemd/system/jellyfin.service", nil); err != nil {
		return err
	}
	if err := p.claimProject(ctx, l, identity, false); err != nil {
		return err
	}
	if err := p.upload(ctx, l, "/opt/jellyfin/.env", environment); err != nil {
		return err
	}
	if _, err := p.run(ctx, l, "pull pinned Jellyfin image", "docker pull "+quote(s.Image), nil); err != nil {
		return err
	}
	// For initial onboarding, Docker publishes only guest loopback, and has no
	// auto-restart policy. A failed auth/onboarding or guest reboot cannot expose
	// the wizard to the LAN. Never relax this boundary on an unsuccessful retry.
	initial := strings.Replace(jellyfinCompose, "    network_mode: host\n", "    ports:\n      - \"127.0.0.1:8096:8096\"\n", 1)
	initial = strings.Replace(initial, "    restart: unless-stopped\n", "    restart: \"no\"\n", 1)
	// Always authenticate behind loopback first, even if a stale onboarding
	// marker survived an out-of-band configuration reset.
	if err := p.upload(ctx, l, "/opt/jellyfin/compose.yaml", []byte(initial)); err != nil {
		return err
	}
	if err := p.upload(ctx, l, "/opt/jellyfin/onboard.py", []byte(jellyfinOnboard)); err != nil {
		return err
	}
	if _, err := p.run(ctx, l, "start pinned Jellyfin for authenticated onboarding", compose(l)+" up -d --pull never --remove-orphans", nil); err != nil {
		return err
	}
	if _, err := p.run(ctx, l, "wait for real Jellyfin HTTP health", "set -eu; for attempt in $(seq 1 60); do if curl --fail --silent --max-time 2 http://127.0.0.1:8096/health >/dev/null; then exit 0; fi; sleep 2; done; exit 1", nil); err != nil {
		return err
	}
	input, err := json.Marshal(map[string]any{"admin": s.Admin, "roku": s.Viewer, "allowRemoteAccess": s.AllowRemoteAccess})
	if err != nil {
		return fmt.Errorf("apps: cannot encode onboarding credentials")
	}
	if _, err := p.run(ctx, l, "authenticate and configure Jellyfin administrator/viewer, GPU transcoding and read-only media library", "python3 /opt/jellyfin/onboard.py", input); err != nil {
		return err
	}
	if err := p.upload(ctx, l, "/opt/jellyfin/compose.yaml", []byte(jellyfinCompose)); err != nil {
		return err
	}
	// The example service starts exactly this project, and its down operation
	// intentionally does not contain --volumes. Config and media are retained.
	if _, err := p.run(ctx, l, "enable onboarded Jellyfin and preserve its data", "set -eu; touch /opt/jellyfin/.openctl-onboarded; "+compose(l)+" up -d --pull never --remove-orphans; systemctl enable --now jellyfin.service", nil); err != nil {
		return err
	}
	return nil
}

func rebootRequired() error {
	return fmt.Errorf("apps: guest reboot required after Docker/NVIDIA package installation; explicitly reboot the dedicated guest, then retry Apply with the same manifest (configuration, image pin and media are preserved; no automatic reboot was performed)")
}

func mediaCheck(l locator) string {
	directory := quote(mediaDirectory(l))
	return "findmnt -rn -T " + directory + " -t cifs -O ro >/dev/null && test \"$(findmnt -rn -T " + directory + " -o SOURCE)\" = " + quote("//"+l.NASServer+"/"+l.NASShare) + " && test -d " + directory
}

func mediaDirectory(l locator) string { return path.Join("/mnt/synology", l.NASMediaPath) }
