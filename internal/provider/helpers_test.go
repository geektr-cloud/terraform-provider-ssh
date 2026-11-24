package provider

import (
	"fmt"
	"net"
	"testing"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

// testServerDefaults builds provider defaults that connect to an in-process
// test server.
func testServerDefaults(t *testing.T, server *testsupport.Server) *providerDefaults {
	t.Helper()
	host, port, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	return &providerDefaults{profile: Profile{
		Host:                  stringValue(host),
		Port:                  stringValue(port),
		User:                  stringValue("tester"),
		PrivateKey:            stringValue(server.PrivateKeyPEM(t)),
		InsecureIgnoreHostKey: boolValue(true),
		Timeout:               stringValue("5s"),
		RetryDelay:            stringValue("50ms"),
	}}
}

// testAccProviderHCL returns a provider block carrying the test server
// defaults. Configurations under test omit conn and inherit these defaults.
func testAccProviderHCL(t *testing.T, server *testsupport.Server) string {
	t.Helper()
	host, port, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`
provider "ssh" {
  host        = %q
  port        = %q
  user        = "tester"
  private_key = <<-EOT
%s
EOT
  insecure_ignore_host_key = true
  timeout                  = "10s"
  retry_delay              = "50ms"
}
`, host, port, server.PrivateKeyPEM(t))
}

func testAccProviderConfig(providerBlock, body string) string {
	return providerBlock + "\n" + body
}
