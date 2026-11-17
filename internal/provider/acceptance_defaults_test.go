package provider

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

func TestAccInlineConnectionDefaults(t *testing.T) {
	server := testsupport.Start(t)
	host, port, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	file := filepath.Join(base, "inline.txt")

	config := fmt.Sprintf(`
provider "ssh" {}

resource "ssh_resource" "inline" {
  conn = {
    host        = %q
    port        = %q
    user        = "tester"
    private_key = <<-EOT
%s
EOT
    host_key    = %q
  }

  file {
    path    = %q
    content = "inline\n"
    mode    = "0600"
  }
}
`, host, port, server.PrivateKeyPEM(t), server.HostKeyString(), file)

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
					resource.TestCheckResourceAttr("ssh_resource.inline", "file.0.content", "inline\n"),
					func(_ *terraform.State) error {
						data, err := os.ReadFile(file)
						if err != nil {
							return err
						}
						if string(data) != "inline\n" {
							return fmt.Errorf("content = %q", data)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccTopLevelOwnershipDefaults(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root-created test files would not prove inheritance")
	}
	server := testsupport.Start(t)
	conn := testAccConn(t, server)
	base := t.TempDir()
	file := filepath.Join(base, "owned.txt")
	uid, gid := os.Getuid(), os.Getgid()

	config := testAccProviderConfig(conn, fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn = data.ssh_conn.test.conn
  uid  = %d
  gid  = %d

  file {
    path    = %q
    content = "owned\n"
  }
}
`, uid, gid, file))

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
					resource.TestCheckResourceAttr("ssh_resource.test", "file.0.uid", fmt.Sprint(uid)),
					resource.TestCheckResourceAttr("ssh_resource.test", "file.0.gid", fmt.Sprint(gid)),
					func(_ *terraform.State) error {
						info, err := os.Stat(file)
						if err != nil {
							return err
						}
						stat := info.Sys().(*syscall.Stat_t)
						if int(stat.Uid) != uid || int(stat.Gid) != gid {
							return fmt.Errorf("ownership = %d:%d, want %d:%d", stat.Uid, stat.Gid, uid, gid)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccDefaultModesAndUmask(t *testing.T) {
	server := testsupport.Start(t)
	conn := testAccConn(t, server)
	base := t.TempDir()
	dir := filepath.Join(base, "defaults")
	file := filepath.Join(dir, "file")

	config := testAccProviderConfig(conn, fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn  = data.ssh_conn.test.conn
  umask = "027"

  directory {
    path = %q
  }

  file {
    path    = %q
    content = "default mode\n"
  }
}
`, dir, file))

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
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ssh_resource.test", "directory.0.mode", "0750"),
					resource.TestCheckResourceAttr("ssh_resource.test", "file.0.mode", "0640"),
					func(_ *terraform.State) error {
						for p, want := range map[string]os.FileMode{dir: 0750, file: 0640} {
							info, err := os.Stat(p)
							if err != nil {
								return err
							}
							if info.Mode().Perm() != want {
								return fmt.Errorf("%s mode = %04o, want %04o", p, info.Mode().Perm(), want)
							}
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
