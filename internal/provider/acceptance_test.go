package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

func testAccFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"ssh": providerserver.NewProtocol6WithError(New("test")()),
	}
}

func TestAccResourceFilesAndDirectories(t *testing.T) {
	server := testsupport.Start(t)
	providerBlock := testAccProviderHCL(t, server)
	base := t.TempDir()
	dir := filepath.Join(base, "dir")
	file := filepath.Join(dir, "hello.txt")
	secret := filepath.Join(base, "secret.txt")
	secretDigest := sha256.Sum256([]byte("s3cr3t\n"))
	wantDigest := hex.EncodeToString(secretDigest[:])

	config := func(content string) string {
		return testAccProviderConfig(providerBlock, fmt.Sprintf(`
resource "ssh_resource" "test" {

  directory {
    path = %q
    mode = "0750"
  }

  file {
    path    = %q
    content = %q
    mode    = "0640"
  }

  file {
    path       = %q
    content_wo = "s3cr3t\n"
    mode       = "0600"
  }
}
`, dir, file, content, secret))
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		CheckDestroy: func(_ *terraform.State) error {
			for _, p := range []string{file, secret, dir} {
				if _, err := os.Stat(p); err == nil {
					return fmt.Errorf("%s still exists after destroy", p)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config("hello\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ssh_resource.test", "directory.0.mode", "0750"),
					resource.TestCheckResourceAttr("ssh_resource.test", "file.0.content", "hello\n"),
					resource.TestCheckResourceAttr("ssh_resource.test", "file.1.content_sha256", wantDigest),
					func(_ *terraform.State) error {
						data, err := os.ReadFile(file)
						if err != nil {
							return err
						}
						if string(data) != "hello\n" {
							return fmt.Errorf("remote content = %q", data)
						}
						info, err := os.Stat(file)
						if err != nil {
							return err
						}
						if info.Mode().Perm() != 0640 {
							return fmt.Errorf("mode = %04o", info.Mode().Perm())
						}
						data, err = os.ReadFile(secret)
						if err != nil {
							return err
						}
						if string(data) != "s3cr3t\n" {
							return fmt.Errorf("secret content = %q", data)
						}
						return nil
					},
				),
			},
			{
				Config: config("world\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ssh_resource.test", "file.0.content", "world\n"),
					func(_ *terraform.State) error {
						data, err := os.ReadFile(file)
						if err != nil {
							return err
						}
						if string(data) != "world\n" {
							return fmt.Errorf("remote content = %q", data)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccDataFileCommandRun(t *testing.T) {
	server := testsupport.Start(t)
	providerBlock := testAccProviderHCL(t, server)
	base := t.TempDir()
	file := filepath.Join(base, "input.txt")
	if err := os.WriteFile(file, []byte("file data\n"), 0600); err != nil {
		t.Fatal(err)
	}

	config := testAccProviderConfig(providerBlock, fmt.Sprintf(`
data "ssh_data" "test" {

  file {
    key  = "file"
    path = %q
    trim = false
  }

  command {
    key     = "command"
    command = ["printf", "%%s", "command data"]
  }

  command {
    key     = "padded"
    command = ["printf", "  padded  "]
  }

  run {
    key    = "run"
    script = "printf '  run data  \\n'"
  }
}

output "file_value" { value = data.ssh_data.test.values["file"] }
output "command_value" { value = data.ssh_data.test.values["command"] }
output "padded_value" { value = data.ssh_data.test.values["padded"] }
output "run_value" { value = data.ssh_data.test.values["run"] }
`, file))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ssh_data.test", "file.0.trim", "false"),
					resource.TestCheckResourceAttr("data.ssh_data.test", "command.1.trim", "true"),
					resource.TestCheckResourceAttr("data.ssh_data.test", "run.0.trim", "true"),
					resource.TestCheckOutput("file_value", "file data\n"),
					resource.TestCheckOutput("command_value", "command data"),
					resource.TestCheckOutput("padded_value", "padded"),
					resource.TestCheckOutput("run_value", "run data"),
				),
			},
		},
	})
}

func TestAccSensitiveDataSource(t *testing.T) {
	server := testsupport.Start(t)
	providerBlock := testAccProviderHCL(t, server)
	base := t.TempDir()
	file := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(file, []byte("secret file\n"), 0600); err != nil {
		t.Fatal(err)
	}

	config := testAccProviderConfig(providerBlock, fmt.Sprintf(`
data "ssh_sensitive_data" "test" {

  file {
    key  = "file"
    path = %q
    trim = false
  }

  command {
    key     = "command"
    command = ["printf", "%%s", "  secret command  "]
  }

  run {
    key    = "run"
    script = "printf '  secret run  \\n'"
  }
}

output "file_value" {
  value     = data.ssh_sensitive_data.test.values["file"]
  sensitive = true
}

output "command_value" {
  value     = data.ssh_sensitive_data.test.values["command"]
  sensitive = true
}

output "run_value" {
  value     = data.ssh_sensitive_data.test.values["run"]
  sensitive = true
}
`, file))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ssh_sensitive_data.test", "file.0.trim", "false"),
					resource.TestCheckResourceAttr("data.ssh_sensitive_data.test", "command.0.trim", "true"),
					resource.TestCheckResourceAttr("data.ssh_sensitive_data.test", "run.0.trim", "true"),
					resource.TestCheckResourceAttr("data.ssh_sensitive_data.test", "values.file", "secret file\n"),
					resource.TestCheckResourceAttr("data.ssh_sensitive_data.test", "values.command", "secret command"),
					resource.TestCheckResourceAttr("data.ssh_sensitive_data.test", "values.run", "secret run"),
					resource.TestCheckNoResourceAttr("data.ssh_sensitive_data.test", "sensitive_values"),
					resource.TestCheckOutput("file_value", "secret file\n"),
					resource.TestCheckOutput("command_value", "secret command"),
					resource.TestCheckOutput("run_value", "secret run"),
				),
			},
		},
	})
}

func TestAccAutoImportAndPathDrift(t *testing.T) {
	server := testsupport.Start(t)
	providerBlock := testAccProviderHCL(t, server)
	base := t.TempDir()
	file := filepath.Join(base, "adopted.txt")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}

	config := testAccProviderConfig(providerBlock, fmt.Sprintf(`
resource "ssh_resource" "test" {

  file {
    path        = %q
    content     = "converged"
    mode        = "0644"
    auto_import = true
  }
}
`, file))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		CheckDestroy: func(_ *terraform.State) error {
			if _, err := os.Stat(file); err == nil {
				return fmt.Errorf("%s still exists", file)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ssh_resource.test", "file.0.auto_import", "true"),
					func(_ *terraform.State) error {
						data, err := os.ReadFile(file)
						if err != nil {
							return err
						}
						if string(data) != "converged" {
							return fmt.Errorf("content = %q", data)
						}
						return nil
					},
				),
			},
			{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}
