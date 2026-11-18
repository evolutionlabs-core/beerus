package connect

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/evolutionlabs-core/beerus"

var participantPackages = []string{
	modulePath + "/internal/bank/participant",
	modulePath + "/internal/nibss/participant",
	modulePath + "/internal/cbn/participant",
}

var limitedImports = map[string][]string{
	modulePath + "/internal/bank/store":  {"Create", "DropDatabases", "List", "Migrate", "Open"},
	modulePath + "/internal/bank/ledger": {"Check", "ErrUnknownAccount", "Migrate"},
	modulePath + "/internal/nibss":       {"ClosureSession", "ConsentEvent", "Create", "Migrate", "Position", "Schedule", "Session", "SessionOK", "SessionPending"},
	modulePath + "/internal/bank":        {"AddMinor", "Customer", "CustomerActive", "ErrBadCursor", "ErrBadLogin", "ErrUnknownCustomer", "ErrUnknownLine", "FeedLine", "GenesisSettlement", "GenesisVault", "MaxPage", "Naira", "Normalise", "ParseNaira", "Totals"},
	modulePath + "/internal/bank/server": {
		"AccountJSON", "AccountRequest", "AllPages", "ClosurePaid", "ClosurePayoutRequest", "CustomerRequest", "Entry", "ErrInvalid", "ErrNoSend", "ErrorLog",
		"Fault", "FaultDown", "FaultID", "FaultPageMax", "FaultRequest", "FaultTimeout", "FaultView", "Invalid", "LedgerCheck", "Link", "ListParams", "LoginRequest",
		"LoginResult", "MaxFaultFor", "MaxPeerBody", "NIPSession", "NewFault", "NewLink", "NoBanksMessage", "NotActiveError", "OpenBanks", "OpenSends", "PeerError",
		"PhaseRequest", "Refused", "State", "Step", "Totals", "TransferBody", "TransferError", "TransferRequest", "TransferResult", "UnavailableError", "WriteBankingError",
	},
}

func TestNoParticipantInCore(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Deps " "}}`, ".").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("list the dependencies: %v: %s", err, exit.Stderr)
		}
		t.Fatalf("list the dependencies: %v", err)
	}
	deps := strings.Fields(string(out))
	for _, pkg := range participantPackages {
		if slices.Contains(deps, pkg) {
			t.Errorf("internal/connect reaches %s", pkg)
		}
	}
}

func TestCoreUsesBankAndNIBSSOnlyThroughTheirClients(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		local := map[string]string{}
		for _, im := range f.Imports {
			path := strings.Trim(im.Path.Value, `"`)
			if path != modulePath+"/internal/bank" && !strings.HasPrefix(path, modulePath+"/internal/bank/") && path != modulePath+"/internal/nibss" && !strings.HasPrefix(path, modulePath+"/internal/nibss/") && !strings.HasPrefix(path, modulePath+"/internal/cbn") {
				continue
			}
			base := filepath.Base(path)
			if im.Name != nil {
				base = im.Name.Name
			}
			local[base] = path
			if _, ok := limitedImports[path]; !ok && path != modulePath+"/internal/bank/consent" {
				t.Errorf("%s imports %s", name, path)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Obj != nil {
				return true
			}
			path, ok := local[id.Name]
			if !ok {
				return true
			}
			if used[path] == nil {
				used[path] = map[string]bool{}
			}
			used[path][sel.Sel.Name] = true
			return true
		})
	}
	paths := make([]string, 0, len(limitedImports))
	for path := range limitedImports {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		for sym := range used[path] {
			if !slices.Contains(limitedImports[path], sym) {
				t.Errorf("internal/connect uses %s.%s", path, sym)
			}
		}
		for _, sym := range limitedImports[path] {
			if !used[path][sym] {
				t.Errorf("internal/connect no longer uses %s.%s; remove it from the list", path, sym)
			}
		}
	}
}
