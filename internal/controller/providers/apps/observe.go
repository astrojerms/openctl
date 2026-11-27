package apps

import (
	"context"
	"encoding/json"
	"net"
	"strings"

	"github.com/openctl/openctl/pkg/protocol"
)

type containerState struct {
	Service  string `json:"Service"`
	State    string `json:"State"`
	Health   string `json:"Health"`
	ExitCode int    `json:"ExitCode"`
}

func parseContainers(output string) ([]containerState, bool) {
	var states []containerState
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, false
	}
	if strings.HasPrefix(output, "[") {
		if json.Unmarshal([]byte(output), &states) != nil {
			return nil, false
		}
	} else {
		for line := range strings.SplitSeq(output, "\n") {
			var c containerState
			if json.Unmarshal([]byte(line), &c) != nil {
				return nil, false
			}
			states = append(states, c)
		}
	}
	ready := len(states) > 0
	for _, c := range states {
		if c.Service == "" || c.State == "" {
			return nil, false
		}
		if c.State != "running" || (c.Health != "" && c.Health != "healthy") || c.ExitCode != 0 {
			ready = false
		}
	}
	return states, ready
}

func (p *Provider) observe(ctx context.Context, kind, name string, l locator) *protocol.Resource {
	target := map[string]any{"host": l.Target.Host, "port": l.Target.Port, "user": l.Target.User, "privateKeyFile": l.Target.PrivateKeyFile, "knownHostsFile": l.Target.KnownHostsFile, "sudo": l.Target.Sudo}
	r := &protocol.Resource{APIVersion: APIVersion, Kind: kind, Metadata: protocol.ResourceMetadata{Name: name}, Spec: map[string]any{"target": target, "project": l.Project, "directory": l.Directory, "managedRunning": false}, Status: map[string]any{}}
	if l.PublicEndpoint != "" {
		r.Spec["publicEndpoint"] = l.PublicEndpoint
	}
	checks := map[string]any{}
	set := func(key, state, message string) {
		checks[key] = map[string]any{"state": state, "message": message}
	}
	r.Status["checks"] = checks
	r.Status["ready"], r.Status["phase"], r.Status["state"] = false, "degraded", "degraded"
	r.Status["message"] = "Application is not ready; inspect the independent readiness checks."
	if kind == "Jellyfin" {
		r.Status["url"] = "http://" + net.JoinHostPort(l.Target.Host, "8096")
	} else {
		r.Status["url"] = l.Probe.URL
	}
	reachable := false
	if _, err := p.run(ctx, l, "observe verified guest SSH", "true", nil); err != nil {
		set("vm", "not_ready", "Guest SSH unavailable or host key unverified")
	} else {
		reachable = true
		set("vm", "ready", "Verified SSH guest is reachable")
	}
	allReady := reachable
	if reachable {
		out, err := p.run(ctx, l, "observe Compose containers", p.ownership(kind, name, l)+"; "+projectGuard(l, owner(p.controllerID, kind, name))+"; "+compose(l)+" ps --all --format json", nil)
		containers, ready := parseContainers(out)
		ready = ready && err == nil
		r.Spec["managedRunning"] = ready
		if kind == "Jellyfin" && err == nil {
			image, err := p.run(ctx, l, "observe actual Jellyfin container image", "id=$("+compose(l)+" ps -aq jellyfin); test -n \"$id\"; docker inspect --format "+quote("{{.Config.Image}}")+" \"$id\"", nil)
			if err == nil && strings.TrimSpace(image) != "" {
				r.Spec["image"] = strings.TrimSpace(image)
			}
		}
		allReady = allReady && ready
		state := "not_ready"
		if err == nil && containers == nil {
			state = "unknown"
		} else if ready {
			state = "ready"
		}
		set("application", state, "All application containers must be running with healthy (or absent) container health checks")
		observed := make([]map[string]any, 0, len(containers))
		for _, c := range containers {
			observed = append(observed, map[string]any{"service": c.Service, "state": c.State, "health": c.Health, "exitCode": c.ExitCode})
		}
		r.Status["containers"] = observed
	} else {
		set("application", "unknown", "Restore verified guest SSH access to inspect application containers")
	}
	httpReady := false
	jellyfinID := ""
	if reachable {
		command := "curl --fail --silent --show-error --max-time 10 --output /dev/null -- " + quote(l.Probe.URL)
		if kind == "Jellyfin" {
			script := "import json,urllib.request; r=json.load(urllib.request.urlopen('http://127.0.0.1:8096/System/Info/Public',timeout=10)); assert r.get('StartupWizardCompleted') is True"
			if l.PublicEndpoint != "" {
				script += "; print(json.dumps({'Id': r.get('Id'), 'StartupWizardCompleted': True}))"
			}
			command += "; python3 -c " + quote(script)
		}
		out, err := p.run(ctx, l, "probe application HTTP", "set -e; "+command, nil)
		httpReady = err == nil
		if httpReady && kind == "Jellyfin" && l.PublicEndpoint != "" {
			jellyfinID, httpReady = jellyfinServerID([]byte(out))
		}
		state := "not_ready"
		if httpReady {
			state = "ready"
		}
		set("http", state, "Application HTTP must succeed; Jellyfin requires completed onboarding and a readable server identity when public exposure is configured")
	} else {
		set("http", "unknown", "Restore verified guest SSH access to probe application HTTP and onboarding completion")
	}
	allReady = allReady && httpReady
	if kind == "Jellyfin" {
		mediaState, mediaMessage := "unknown", "Restore verified guest SSH access to inspect this application dependency"
		if reachable {
			mediaState, mediaMessage = p.observeJellyfinMedia(ctx, l)
		}
		set("media", mediaState, mediaMessage)
		allReady = allReady && mediaState == "ready"
		checksToRun := []struct{ key, command, message string }{
			{"gpu", "nvidia-smi >/dev/null && docker exec $(" + compose(l) + " ps -q jellyfin) nvidia-smi >/dev/null", "NVIDIA GPU must work in both guest and Jellyfin container"},
			{"onboarding", "test -f /opt/jellyfin/.openctl-onboarded", "Authenticated onboarding must finish before the application is exposed"},
		}
		for _, check := range checksToRun {
			ready := false
			state := "unknown"
			message := "Restore verified guest SSH access to inspect this application dependency"
			if reachable {
				_, err := p.run(ctx, l, "observe "+check.key, check.command, nil)
				ready = err == nil
				state, message = "not_ready", check.message
				if ready {
					state = "ready"
				}
			}
			set(check.key, state, message)
			allReady = allReady && ready
		}
		if reachable {
			_, err := p.run(ctx, l, "check guest reboot requirement", "test -e /var/run/reboot-required", nil)
			if err == nil {
				allReady = false
				r.Status["phase"], r.Status["state"] = "reboot-required", "reboot-required"
				r.Status["message"] = "Guest reboot required after driver/package installation; reboot explicitly, then retry Apply. No automatic reboot was performed."
			}
		}
	}
	if l.PublicEndpoint == "" {
		set("publicEndpoint", "not_configured", "Not configured; no endpoint was published")
	} else {
		state, message := probePublic(ctx, l.PublicEndpoint, kind, jellyfinID)
		set("publicEndpoint", state, message)
		allReady = allReady && state == "ready"
	}
	r.Status["ready"] = allReady
	if allReady {
		r.Status["phase"], r.Status["state"], r.Status["message"] = "ready", "ready", "Application HTTP, containers and all configured dependency checks are ready."
	}
	return r
}

// Filter on the guest, before raw logs cross into the controller. Container
// environment values are a common accidental credential disclosure source.
func redactedLogs(l locator) string {
	script := `import json,subprocess,sys
base=json.loads(sys.argv[1])
ids=subprocess.check_output(base+['ps','-aq'],text=True).split()
values=[]
if ids:
    for c in json.loads(subprocess.check_output(['docker','inspect']+ids,text=True)):
        values += [e.split('=',1)[1] for e in c.get('Config',{}).get('Env',[]) if '=' in e and e.split('=',1)[1]]
result=subprocess.run(base+['logs','--no-color','--tail','200'],stdout=subprocess.PIPE,stderr=subprocess.STDOUT,check=True,text=True).stdout
for value in sorted(set(values),key=len,reverse=True):
    result=result.replace(value,'[REDACTED]')
sys.stdout.write(result)
`
	args, _ := json.Marshal([]string{"docker", "compose", "--project-name", l.Project, "--project-directory", l.Directory, "--env-file", l.Directory + "/.env", "-f", l.Directory + "/compose.yaml"})
	return "python3 -c " + quote(script) + " " + quote(string(args))
}
