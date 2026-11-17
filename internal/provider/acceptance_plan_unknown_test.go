package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

// timestamp() is unknown during plan and known during apply, which exercises
// the configuration-as-source-of-truth paths without fabricating a provider.
func TestAccConfigValuesUnknownAtPlan(t *testing.T) {
	server := testsupport.Start(t)
	conn := testAccConn(t, server)
	altConn := strings.Replace(conn, `data "ssh_conn" "test"`, `data "ssh_conn" "alt"`, 1)
	conns := conn + "\n" + altConn
	base := t.TempDir()

	config := testAccProviderConfig(conns, fmt.Sprintf(`
resource "ssh_resource" "unknown" {
  conn  = timestamp() != "" ? data.ssh_conn.test.conn : data.ssh_conn.alt.conn
  umask = timestamp() != "" ? "027" : "022"

  directory {
    path        = timestamp() != "" ? %[1]q : "%[1]s-other"
    auto_import = timestamp() != ""
  }

  file {
    path        = timestamp() != "" ? %[2]q : "%[2]s-other"
    content     = timestamp()
    auto_import = timestamp() != ""
  }
}
`, filepath.Join(base, "dir"), filepath.Join(base, "file-x")))

	dir := filepath.Join(base, "dir")
	file := filepath.Join(base, "file-x")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		CheckDestroy: func(_ *terraform.State) error {
			for _, p := range []string{file, dir} {
				if _, err := os.Stat(p); err == nil {
					return fmt.Errorf("%s still exists", p)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:             config,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ssh_resource.unknown", "directory.0.path", dir),
					resource.TestCheckResourceAttr("ssh_resource.unknown", "directory.0.mode", "0750"),
					resource.TestCheckResourceAttr("ssh_resource.unknown", "directory.0.auto_import", "true"),
					resource.TestCheckResourceAttr("ssh_resource.unknown", "file.0.path", file),
					resource.TestCheckResourceAttr("ssh_resource.unknown", "file.0.mode", "0640"),
					resource.TestCheckResourceAttr("ssh_resource.unknown", "file.0.auto_import", "true"),
					resource.TestCheckResourceAttrSet("ssh_resource.unknown", "file.0.content"),
					func(state *terraform.State) error {
						want := state.RootModule().Resources["ssh_resource.unknown"].Primary.Attributes["file.0.content"]
						data, err := os.ReadFile(file)
						if err != nil {
							return err
						}
						if string(data) != want {
							return fmt.Errorf("remote content = %q, state = %q", data, want)
						}
						info, err := os.Stat(file)
						if err != nil {
							return err
						}
						if info.Mode().Perm() != 0640 {
							return fmt.Errorf("file mode = %04o", info.Mode().Perm())
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccInvalidOutputLimitHasNoSideEffects(t *testing.T) {
	server := testsupport.Start(t)
	conn := testAccConn(t, server)
	file := filepath.Join(t.TempDir(), "never.txt")
	t.Setenv(outputLimitEnv, "not-a-number")

	config := testAccProviderConfig(conn, fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn = data.ssh_conn.test.conn
  file {
    path    = %q
    content = "content"
  }
}
`, file))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile("Invalid output limit"),
			},
		},
	})
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("file was created despite invalid output limit: %v", err)
	}
}

func TestAccOversizedPlainContentHasNoSideEffects(t *testing.T) {
	server := testsupport.Start(t)
	conn := testAccConn(t, server)
	file := filepath.Join(t.TempDir(), "too-big.txt")
	t.Setenv(outputLimitEnv, "4")

	config := testAccProviderConfig(conn, fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn = data.ssh_conn.test.conn
  file {
    path    = %q
    content = "12345"
  }
}
`, file))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile("exceeding the configured limit"),
			},
		},
	})
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("file was created despite oversized plain content: %v", err)
	}
}

func TestAccWriteOnlyContentIsNotLimited(t *testing.T) {
	server := testsupport.Start(t)
	conn := testAccConn(t, server)
	file := filepath.Join(t.TempDir(), "write-only.txt")
	t.Setenv(outputLimitEnv, "4")

	config := testAccProviderConfig(conn, fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn = data.ssh_conn.test.conn
  file {
    path       = %q
    content_wo = "123456789"
    mode       = "0600"
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
					func(_ *terraform.State) error {
						data, err := os.ReadFile(file)
						if err != nil {
							return err
						}
						if strings.TrimSpace(string(data)) != "123456789" {
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
