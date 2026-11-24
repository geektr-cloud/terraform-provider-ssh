package provider

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

func testAccProviderHCLWithKey(t *testing.T, server *testsupport.Server, key string) string {
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
`, host, port, key)
}

// TestAccJSONOverrideInheritsProviderDefaults uses a partial JSON override and
// the provider function with provider defaults.
func TestAccJSONOverrideInheritsProviderDefaults(t *testing.T) {
	server := testsupport.Start(t)
	providerBlock := testAccProviderHCL(t, server)
	host, port, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	overrideFile := filepath.Join(base, "override.txt")
	functionFile := filepath.Join(base, "function.txt")

	config := testAccProviderConfig(providerBlock, fmt.Sprintf(`
resource "ssh_resource" "override" {
  conn = jsonencode({
    user = "tester"
  })

  file {
    path    = %q
    content = "override\n"
    mode    = "0600"
  }
}

resource "ssh_resource" "function" {
  conn = provider::ssh::connection(%q, "tester", %q)

  file {
    path    = %q
    content = "function\n"
    mode    = "0600"
  }
}

data "ssh_data" "override" {
  conn = jsonencode({ user = "tester" })

  depends_on = [ssh_resource.override]

  file {
    key  = "content"
    path = %q
  }
}

output "data_value" {
  value = data.ssh_data.override.values["content"]
}
`, overrideFile, host, port, functionFile, overrideFile))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		CheckDestroy: func(_ *terraform.State) error {
			for _, p := range []string{overrideFile, functionFile} {
				if _, err := os.Stat(p); err == nil {
					return fmt.Errorf("%s still exists", p)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ssh_resource.override", "conn", `{"user":"tester"}`),
					resource.TestCheckResourceAttrSet("ssh_resource.override", "connection_identity"),
					resource.TestCheckResourceAttr("ssh_resource.function", "conn", fmt.Sprintf(`{"host":%q,"user":"tester","port":%q}`, host, port)),
					resource.TestCheckOutput("data_value", "override"),
				),
			},
		},
	})
}

// TestAccConnectionIdentityReplacement checks that identity changes force
// replacement while credential changes do not.
func TestAccConnectionIdentityReplacement(t *testing.T) {
	server := testsupport.Start(t)
	providerBlock := testAccProviderHCL(t, server)
	_, port, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "identity.txt")

	config := func(conn string) string {
		return testAccProviderConfig(providerBlock, fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn = %s

  file {
    path    = %q
    content = "identity\n"
    mode    = "0600"
  }
}
`, conn, target))
	}
	sameIdentity := config(`jsonencode({ user = "tester", port = ` + fmt.Sprintf("%q", port) + ` })`)
	changedIdentity := config(`jsonencode({ user = "other" })`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		CheckDestroy: func(_ *terraform.State) error {
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("%s still exists", target)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: sameIdentity,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("ssh_resource.test", "connection_identity"),
				),
			},
			{
				Config: sameIdentity,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ssh_resource.test", plancheck.ResourceActionNoop),
					},
				},
			},
			{
				Config: changedIdentity,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ssh_resource.test", plancheck.ResourceActionReplace),
					},
				},
			},
		},
	})
}

// TestAccCredentialRotation keeps the identity and replaces only the provider
// credential.
func TestAccCredentialRotation(t *testing.T) {
	server := testsupport.Start(t)
	target := filepath.Join(t.TempDir(), "rotate.txt")
	body := fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn = jsonencode({ user = "tester" })

  file {
    path    = %q
    content = "rotate\n"
    mode    = "0600"
  }
}
`, target)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		CheckDestroy: func(_ *terraform.State) error {
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("%s still exists", target)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: testAccProviderConfig(testAccProviderHCLWithKey(t, server, server.PrivateKeyPEM(t)), body),
			},
			{
				Config:             testAccProviderConfig(testAccProviderHCLWithKey(t, server, server.AltPrivateKeyPEM(t)), body),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccDestroyWithPriorOverrideAndProviderDefaults destroys using the prior
// JSON override plus the current provider defaults.
func TestAccDestroyWithPriorOverrideAndProviderDefaults(t *testing.T) {
	server := testsupport.Start(t)
	host, port, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	providerBlock := testAccProviderHCLWithKey(t, server, server.PrivateKeyPEM(t))
	target := filepath.Join(t.TempDir(), "destroy.txt")

	config := testAccProviderConfig(providerBlock, fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn = jsonencode({
    host = %q
    port = %q
    user = "tester"
  })

  file {
    path    = %q
    content = "destroy\n"
    mode    = "0600"
  }
}
`, host, port, target))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		CheckDestroy: func(_ *terraform.State) error {
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("%s still exists", target)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config},
		},
	})
}

// TestAccDestroyWithCompleteJSONOverride uses an empty provider block: the
// prior JSON override must be sufficient for destroy.
func TestAccDestroyWithCompleteJSONOverride(t *testing.T) {
	server := testsupport.Start(t)
	host, port, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "self-contained.txt")

	config := testAccProviderConfig(`provider "ssh" {}`, fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn = jsonencode({
    host        = %q
    port        = %q
    user        = "tester"
    private_key = <<-EOT
%s
EOT
    insecure_ignore_host_key = true
  })

  file {
    path    = %q
    content = "self-contained\n"
    mode    = "0600"
  }
}
`, host, port, server.PrivateKeyPEM(t), target))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		CheckDestroy: func(_ *terraform.State) error {
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("%s still exists", target)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config},
		},
	})
}

// TestAccPartialProviderDefaults lets the provider supply only credentials and
// host-key policy while the JSON override supplies the target.
func TestAccPartialProviderDefaults(t *testing.T) {
	server := testsupport.Start(t)
	host, port, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "partial.txt")
	providerBlock := fmt.Sprintf(`
provider "ssh" {
  private_key              = <<-EOT
%s
EOT
  insecure_ignore_host_key = true
}
`, server.PrivateKeyPEM(t))
	config := testAccProviderConfig(providerBlock, fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn = jsonencode({
    host = %q
    port = %q
    user = "tester"
  })

  file {
    path    = %q
    content = "partial\n"
    mode    = "0600"
  }
}
`, host, port, target))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		CheckDestroy: func(_ *terraform.State) error {
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("%s still exists", target)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config},
		},
	})
}

// TestAccProviderConfigureDoesNotConnect configures an unreachable provider
// host and expects only the configuration error, proving Configure does not
// dial.
func TestAccProviderConfigureDoesNotConnect(t *testing.T) {
	config := `
provider "ssh" {
  host                     = "192.0.2.1"
  user                     = "nobody"
  private_key              = "not-a-real-key"
  insecure_ignore_host_key = true
}

resource "ssh_resource" "invalid" {
  file {
    path    = "relative"
    content = "x"
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile("Invalid path"),
			},
		},
	})
}
