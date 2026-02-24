package main

type ProcessRequest struct {
	Command       string `json:"command"`
	WorkingDir    string `json:"working_dir"`
	Image         string `json:"image,omitempty"`
	ImageUsername string `json:"image_username,omitempty"`
	ImagePassword string `json:"image_password,omitempty"`
}

type Process struct {
	ID         string `json:"id"` // format: instanceid_processid
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir"`
	Image      string `json:"image,omitempty"`
	RootfsPath string `json:"rootfs_path,omitempty"`
	Status     string `json:"status"` // running, completed, failed
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ImageInfo holds parsed image reference information
type ImageInfo struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string // image config digest (sha256)
	Layers     []LayerInfo
}

// LayerInfo holds information about a single image layer
type LayerInfo struct {
	Digest    string
	Size      int64
	LocalPath string
}
