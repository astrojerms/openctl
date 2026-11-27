package client

// GuestIPError retains the reason an address observation failed. Its message is
// safe for operators; Unwrap retains provider details for internal inspection.
type GuestIPError struct {
	code  string
	cause error
}

func (e *GuestIPError) Code() string {
	switch e.code {
	case "vm_nic_unavailable", "guest_agent_unavailable", "guest_agent_invalid_response", "guest_ip_ambiguous":
		return e.code
	default:
		return "guest_ip_unavailable"
	}
}

func (e *GuestIPError) Error() string {
	switch e.code {
	case "vm_nic_unavailable":
		return "Read the VM network configuration from the controller and verify a configured NIC has a valid MAC address."
	case "guest_agent_unavailable":
		return "Check guest-agent availability and the controller's permission and connectivity to read guest network interfaces."
	case "guest_agent_invalid_response":
		return "Check the guest agent and the controller's guest network-interface read; the response could not be decoded."
	case "guest_ip_ambiguous":
		return "Multiple usable IPv4 addresses were observed on configured VM NICs; set spec.target.host explicitly for the intended application target."
	default:
		return "Check guest networking and the controller's guest-agent read for a usable IPv4 address on a configured VM NIC."
	}
}

func (e *GuestIPError) Unwrap() error { return e.cause }

func guestIPFailure(code string, cause error) error {
	return &GuestIPError{code: code, cause: cause}
}
