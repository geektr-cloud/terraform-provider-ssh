package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

func TestAccEphemeralWriteOnly(t *testing.T) {
	server := testsupport.Start(t)
	conn := testAccConn(t, server)
	base := t.TempDir()
	target := filepath.Join(base, "ephemeral.txt")
	want := sha256.Sum256([]byte("ephemeral-secret"))
	wantDigest := hex.EncodeToString(want[:])

	config := testAccProviderConfig(conn, fmt.Sprintf(`
ephemeral "ssh_data" "secret" {
  conn = data.ssh_conn.test.conn
  command {
    key     = "secret"
    command = ["printf", "%%s", "  ephemeral-secret  "]
  }
}

resource "ssh_resource" "test" {
  conn = data.ssh_conn.test.conn
  file {
    path       = %q
    content_wo = ephemeral.ssh_data.secret.values["secret"]
    mode       = "0600"
  }
}
`, target))

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
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ssh_resource.test", "file.0.content_sha256", wantDigest),
					func(_ *terraform.State) error {
						data, err := os.ReadFile(target)
						if err != nil {
							return err
						}
						if string(data) != "ephemeral-secret" {
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

func TestAccWriteOnlyDriftRepair(t *testing.T) {
	server := testsupport.Start(t)
	conn := testAccConn(t, server)
	base := t.TempDir()
	target := filepath.Join(base, "drift.txt")

	config := testAccProviderConfig(conn, fmt.Sprintf(`
resource "ssh_resource" "test" {
  conn = data.ssh_conn.test.conn
  file {
    path       = %q
    content_wo = "original-secret"
    mode       = "0600"
  }
}
`, target))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccFactories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("ssh_resource.test", "file.0.content_sha256", digestOf("original-secret")),
			},
			{
				Config: config,
				PreConfig: func() {
					if err := os.WriteFile(target, []byte("tampered"), 0600); err != nil {
						t.Fatalf("tamper: %v", err)
					}
				},
				Check: func(_ *terraform.State) error {
					data, err := os.ReadFile(target)
					if err != nil {
						return err
					}
					if string(data) != "original-secret" {
						return fmt.Errorf("content = %q", data)
					}
					return nil
				},
			},
		},
	})
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
