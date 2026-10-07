package systemd

import (
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m, config.OverrideVars...) }
