package install

// ImageRepository is the registry path the release workflow publishes and every
// Docker install pulls. The workflow names the same path; change both together.
// It is a constant rather than a state field so an existing install migrates by
// repairing: only the tag is recorded.
const ImageRepository = "ghcr.io/amirashouri/tasktracker-server"

// ImageRef is the image reference for one release tag.
func ImageRef(tag string) string {
	return ImageRepository + ":" + tag
}
