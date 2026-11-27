package apps

import (
	"context"
	"encoding/json"
	"strings"
)

// Observe the installed container, not the desired Compose file. No mount,
// container configuration, media content or application state is changed.
func (p *Provider) observeJellyfinMedia(ctx context.Context, l locator) (string, string) {
	directory := mediaDirectory(l)
	quotedDirectory := quote(directory)
	hostCheck := mediaCheck(l) + " && setpriv --reuid=1000 --regid=1000 --clear-groups test -r " + quotedDirectory + " && setpriv --reuid=1000 --regid=1000 --clear-groups test -x " + quotedDirectory
	if _, err := p.run(ctx, l, "observe declared read-only NAS media", hostCheck, nil); err != nil {
		return "not_ready", "Declared read-only CIFS media mount and media directory must be accessible to UID 1000"
	}

	const unverified = "Jellyfin container media binding is unverified; inspect the running container and its /media mount"
	projection := `{"id":{{json .Id}},"mounts":{{json .Mounts}}}`
	command := "set -e; id=$(" + compose(l) + " ps -q jellyfin); test -n \"$id\"; docker inspect --format " + quote(projection) + " \"$id\""
	out, err := p.run(ctx, l, "observe actual Jellyfin container media binding", command, nil)
	if err != nil {
		return "unknown", unverified
	}
	var observed struct {
		ID     string `json:"id"`
		Mounts []struct {
			Type        string `json:"Type"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			ReadWrite   *bool  `json:"RW"`
		} `json:"mounts"`
	}
	if json.Unmarshal([]byte(out), &observed) != nil || !validContainerID(observed.ID) || observed.Mounts == nil {
		return "unknown", unverified
	}
	const mismatch = "Jellyfin /media must be a read-only bind of the declared NAS media directory without nested mounts; inspect container configuration before repairing it"
	index := -1
	for i := range observed.Mounts {
		if observed.Mounts[i].Destination == "" {
			return "unknown", unverified
		}
		if strings.HasPrefix(observed.Mounts[i].Destination, "/media/") {
			return "not_ready", mismatch
		}
		if observed.Mounts[i].Destination == "/media" {
			if index != -1 {
				return "unknown", unverified
			}
			index = i
		}
	}
	if index == -1 {
		return "not_ready", mismatch
	}
	mount := &observed.Mounts[index]
	if mount.Type == "" || mount.Source == "" {
		return "unknown", unverified
	}
	if mount.Type != "bind" || mount.Source != directory {
		return "not_ready", mismatch
	}
	if mount.ReadWrite == nil {
		return "unknown", unverified
	}
	if *mount.ReadWrite {
		return "not_ready", mismatch
	}
	// Use the exact inspected ID: resolving the service again could test access
	// in a replacement with a different binding. These tests never write media.
	command = "docker exec --user 1000:1000 -- " + quote(observed.ID) + " /bin/sh -c " + quote("test -r /media && test -x /media")
	if _, err := p.run(ctx, l, "probe media access inside the inspected Jellyfin container", command, nil); err != nil {
		return "not_ready", "Jellyfin /media must be readable and traversable inside the inspected container as UID 1000"
	}
	return "ready", "Declared read-only NAS media is bound read-only at Jellyfin /media and accessible inside the inspected container as UID 1000"
}

func validContainerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
