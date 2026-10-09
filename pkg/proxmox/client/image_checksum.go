package client

import (
	"fmt"
	"strings"
)

// ImageChecksum is a validated download digest. Its zero value disables checksum
// verification; nonzero values must be constructed with ParseImageChecksum.
type ImageChecksum struct {
	algorithm string
	digest    string
}

// ParseImageChecksum accepts algorithm:hex-digest for Proxmox's supported
// algorithms. It validates the complete digest and canonicalizes hexadecimal case.
func ParseImageChecksum(value string) (ImageChecksum, error) {
	if value == "" {
		return ImageChecksum{}, nil
	}
	algorithm, digest, ok := strings.Cut(value, ":")
	if !ok {
		return ImageChecksum{}, fmt.Errorf("checksum must use algorithm:hex-digest format")
	}
	var length int
	switch algorithm {
	case "md5":
		length = 32
	case "sha1":
		length = 40
	case "sha224":
		length = 56
	case "sha256":
		length = 64
	case "sha384":
		length = 96
	case "sha512":
		length = 128
	default:
		return ImageChecksum{}, fmt.Errorf("unsupported checksum algorithm %q", algorithm)
	}
	if len(digest) != length {
		return ImageChecksum{}, fmt.Errorf("%s checksum must contain %d hexadecimal characters", algorithm, length)
	}
	for i := range len(digest) {
		b := digest[i]
		if (b < '0' || b > '9') && (b < 'a' || b > 'f') && (b < 'A' || b > 'F') {
			return ImageChecksum{}, fmt.Errorf("%s checksum contains a non-hexadecimal character", algorithm)
		}
	}
	return ImageChecksum{algorithm: algorithm, digest: strings.ToLower(digest)}, nil
}

// TemplateTag records the digest verified before importing a cloud-image template.
// Empty checksums deliberately have no provenance tag.
func (c ImageChecksum) TemplateTag() string {
	if c.algorithm == "" {
		return ""
	}
	return "openctl-image-" + c.algorithm + "-" + c.digest
}
