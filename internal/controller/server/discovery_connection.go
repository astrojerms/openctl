package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// discoveryConnectionFailure classifies typed causes only. Error strings, HTTP
// bodies, URLs and certificate fields are untrusted and must never reach the UI.
func discoveryConnectionFailure(err error) *apiv1.TestDiscoveryConnectionResponse {
	code, message := providerReadFailure(err)
	return &apiv1.TestDiscoveryConnectionResponse{FailureCode: code, Message: message}
}

// providerReadFailure returns only static diagnostics derived from typed causes.
func providerReadFailure(err error) (code, message string) {
	code, message = "connection_failed", "Connection failed. Check the active controller-side provider context, endpoint and configuration; saved changes require a controller restart."
	var untrusted x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var httpStatus interface{ HTTPStatusCode() int }
	var timeout net.Error
	var dns *net.DNSError
	var network *net.OpError
	switch {
	case errors.As(err, &untrusted):
		code, message = "tls_untrusted", "Server certificate is not trusted. Obtain a verified CA bundle out of band, install it on the controller host, set caFile and restart the controller."
	case errors.As(err, &hostname):
		code, message = "tls_hostname_mismatch", "Endpoint hostname/IP does not match the server certificate. Correct the endpoint or certificate; do not disable verification."
	case errors.As(err, &invalid):
		code, message = "tls_certificate_invalid", "Server certificate is invalid. Check certificate validity dates, the certificate chain and the controller clock."
	case errors.As(err, &verification), errors.As(err, &record):
		code, message = "tls_handshake_failed", "TLS verification or negotiation failed. Check the HTTPS API port, certificate chain and supported TLS versions."
	case errors.As(err, &httpStatus):
		switch status := httpStatus.HTTPStatusCode(); {
		case status >= 300 && status < 400:
			code, message = "redirect_rejected", "Provider redirected the API request. Configure the direct HTTPS API endpoint; redirects are not followed to protect API credentials."
		case status == 401:
			code, message = "unauthorized", "Provider rejected API authentication. Check the controller-side token ID and secret; restart after changing credentials."
		case status == 403:
			code, message = "permission_denied", "Provider denied read access. Check token/user permissions for the requested resources; read access does not require granting write access."
		case status == 404:
			code, message = "endpoint_not_found", "Provider API route was not found. Check the direct HTTPS API endpoint and any reverse-proxy routing."
		case status == 429:
			code, message = "rate_limited", "Provider is rate limiting requests. Wait before testing again and check other API clients; no automatic retry was attempted."
		case status >= 500 && status < 600:
			code, message = "provider_unavailable", "Provider API returned a server error. Check provider service health and reverse-proxy logs before testing again."
		}
	case errors.Is(err, context.Canceled):
		code, message = "canceled", "Provider read was canceled; it did not establish read access."
	case errors.As(err, &timeout) && timeout.Timeout():
		code, message = "timeout", "Connection timed out. Check controller-to-provider reachability and API responsiveness."
	case errors.As(err, &dns):
		code, message = "dns_failed", "Controller could not resolve the provider endpoint. Check the endpoint hostname and DNS configuration on the controller host."
	case errors.As(err, &network):
		code, message = "endpoint_unreachable", "Controller could not reach the provider. Check routing, firewall rules, the HTTPS API port and provider service availability."
	}
	return code, message
}
