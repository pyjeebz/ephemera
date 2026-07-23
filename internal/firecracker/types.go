package firecracker

// Wire types for the Firecracker REST API. Field names and JSON tags mirror the
// API's swagger definition exactly — this is the contract, so it is written out
// literally rather than derived, and any drift from the VMM shows up here.

// BootSource is PUT /boot-source: which kernel to boot and with what cmdline.
type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args,omitempty"`
	InitrdPath      string `json:"initrd_path,omitempty"`
}

// Drive is PUT /drives/{drive_id}: one virtio-blk device backed by a host file.
// Exactly one drive should set IsRootDevice; the guest sees it as /dev/vda.
type Drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

// MachineConfig is PUT /machine-config: the VM's CPU and memory shape.
type MachineConfig struct {
	VcpuCount  int  `json:"vcpu_count"`
	MemSizeMib int  `json:"mem_size_mib"`
	SMT        bool `json:"smt"`
}

// action is PUT /actions. The only one Phase 1 needs is InstanceStart; the VM is
// stopped by killing the VMM process, not through the API.
type action struct {
	ActionType string `json:"action_type"`
}

// InstanceInfo is GET /: the VMM's own view of itself. Useful as a readiness
// probe — the API answers before the guest has booted.
type InstanceInfo struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	VmmVersion string `json:"vmm_version"`
	AppName    string `json:"app_name"`
}

// apiFault is the error body Firecracker returns on a 4xx/5xx.
type apiFault struct {
	FaultMessage string `json:"fault_message"`
}
