package cli

import (
	"os"
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

func TestMain(m *testing.M) {
	// the documents main embeds
	man, _ := os.ReadFile("../../docs/systemd-compose.1")
	keys, _ := os.ReadFile("../../docs/config.example.yaml")
	docs = Docs{Man: string(man), Keys: string(keys)}
	testenv.Main(m, config.OverrideVars...)
}
