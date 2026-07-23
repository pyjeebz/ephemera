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

// Vsock is PUT /vsock: a virtio-vsock device for host/guest communication.
//
// UDSPath is the Unix socket the VMM creates on the host. Connections to it are
// multiplexed into the guest by a text handshake (see internal/vsock), so one
// path reaches every guest port without any network interface existing.
type Vsock struct {
	VsockID  string `json:"vsock_id,omitempty"`
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

// NetworkInterface is PUT /network-interfaces/{iface_id}: one virtio-net device
// backed by a TAP the host has already created.
//
// HostDevName is a name, not a descriptor — the VMM opens the interface itself,
// so it has to exist and be up before this call, and the VMM's process needs
// permission to open it.
//
// GuestMAC is optional; leaving it empty makes Firecracker invent one, which
// changes on every boot. We always set it, because a stable MAC is what makes a
// packet capture legible.
type NetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
	GuestMAC    string `json:"guest_mac,omitempty"`
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
