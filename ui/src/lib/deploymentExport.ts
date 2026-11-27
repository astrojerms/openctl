import { strToU8, zipSync } from 'fflate';
import type { DeploymentStep } from './jellyfin';
import { withoutRequestConsent } from './retry';
import { resourceToYAML } from './yaml';

export function deploymentManifest(step: DeploymentStep, index: number): { filename: string; yaml: string } {
  // Stage numbers keep initial and final versions of the same resource distinct.
  // Flat ASCII filenames never interpret resource metadata as archive paths.
  const kind = step.resource.kind.toLowerCase().replace(/[^a-z0-9-]/g, '-');
  return {
    filename: `${String(index + 1).padStart(2, '0')}-${kind}.yaml`,
    yaml: resourceToYAML(withoutRequestConsent(step.resource)),
  };
}

export function deploymentArchive(steps: readonly DeploymentStep[]): Uint8Array<ArrayBuffer> {
  const files: Record<string, Uint8Array<ArrayBuffer>> = {};
  for (let index = 0; index < steps.length; index += 1) {
    const file = deploymentManifest(steps[index], index);
    files[file.filename] = strToU8(file.yaml);
  }
  // Reviews are small; store entries without spending time compressing them.
  return zipSync(files, { level: 0 });
}
