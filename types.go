package main

type ProcessRequest struct {
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir"`
}

type Process struct {
	ID         int64  `json:"id"`
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir"`
	Status     string `json:"status"` // running, completed, failed
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
}
