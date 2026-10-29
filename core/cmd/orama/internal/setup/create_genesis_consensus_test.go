package setup

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The pins in create_genesis.go and the values oramad refuses a production genesis for must be one
// number. The chain's side is read from its source, which sits beside core/.
func TestConsensusConstants_equalTheChainsPins(t *testing.T) {
	data, err := os.ReadFile("../../../../testdata/repo/chain/app/consensus_params.go")
	if err != nil {
		t.Skipf("the chain sources are not reachable from here: %v", err)
	}
	src := string(data)
	for name, want := range map[string]string{
		"ConsensusMaxGas":                     consensusMaxGas,
		"ConsensusMaxBlockBytes":              consensusMaxBlockBytes,
		"ConsensusVoteExtensionsEnableHeight": voteExtensionsEnableHeight,
		"ConsensusEvidenceMaxBytes":           evidenceMaxBytes,
	} {
		m := regexp.MustCompile(name + `\s+int64 = ([0-9_ *]+)\n`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("chain/app/consensus_params.go no longer declares %s: update setup/create_genesis.go", name)
		}
		got := int64(1)
		for _, f := range strings.Split(strings.ReplaceAll(m[1], "_", ""), "*") {
			n, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64)
			if err != nil {
				t.Fatalf("%s = %q: %v", name, m[1], err)
			}
			got *= n
		}
		if strconv.FormatInt(got, 10) != want {
			t.Errorf("%s is %d in the chain and %q in setup", name, got, want)
		}
	}
	checkEvidenceWindow(t, src)
	if !regexp.MustCompile(`ConsensusValidatorKeyType = "` + validatorKeyType + `"`).MatchString(src) {
		t.Errorf("chain/app/consensus_params.go no longer pins the validator key type %q: update setup/create_genesis.go", validatorKeyType)
	}
}

func TestApplyConsensusParams_writesTheBlockSizeEvenWhenOramadLeftAnother(t *testing.T) {
	genesis := `{"chain_id":"orama-1","consensus":{"params":{"block":{"max_bytes":"1048576","max_gas":"-1"}}}}`
	out, err := ApplyConsensusParams([]byte(genesis))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeParams(t, out)["block"]["max_bytes"]; got != consensusMaxBlockBytes {
		t.Errorf("max_bytes = %v, want %s", got, consensusMaxBlockBytes)
	}
}

func TestVerifyGenesis_aBlockSizeOtherThanThePinIsRefused(t *testing.T) {
	doc, err := ApplyConsensusParams(genesisFor{chainID: "orama-testnet-6", seats: fakeSeats(2), allowStake: true, faucet: true}.json())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyGenesis(doc, verifySpec(2)); err != nil {
		t.Fatalf("the genesis setup writes was refused: %v", err)
	}
	tampered := []byte(strings.Replace(string(doc), `"max_bytes": "`+consensusMaxBlockBytes+`"`, `"max_bytes": "-1"`, 1))
	if err := VerifyGenesis(tampered, verifySpec(2)); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Errorf("an unlimited block size was accepted: %v", err)
	}
}

func TestVerifyGenesis_aValidatorKeyTypeOrEvidenceWindowOtherThanThePinIsRefused(t *testing.T) {
	doc, err := ApplyConsensusParams(genesisFor{chainID: "orama-testnet-6", seats: fakeSeats(2), allowStake: true, faucet: true}.json())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyGenesis(doc, verifySpec(2)); err != nil {
		t.Fatalf("the genesis setup writes was refused: %v", err)
	}
	for name, tamper := range map[string]struct{ from, to, want string }{
		"a second key type":       {`"ed25519"`, `"ed25519", "secp256k1"`, "pub_key_types"},
		"a shorter evidence age":  {`"max_age_num_blocks": "` + evidenceMaxAgeNumBlocks + `"`, `"max_age_num_blocks": "10"`, "evidence"},
		"a shorter evidence time": {`"max_age_duration": "` + evidenceMaxAgeDurationNanos + `"`, `"max_age_duration": "60000000000"`, "evidence"},
	} {
		bad := []byte(strings.Replace(string(doc), tamper.from, tamper.to, 1))
		if string(bad) == string(doc) {
			t.Fatalf("%s: the genesis has no %s to tamper with:\n%s", name, tamper.from, doc)
		}
		if err := VerifyGenesis(bad, verifySpec(2)); err == nil || !strings.Contains(err.Error(), tamper.want) {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}
}

// The evidence window is 14 days of the chain's blocks: the chain derives it from x/archive's
// retention (DefaultBlocksIn14Days) and the block interval, setup writes the numbers.
func checkEvidenceWindow(t *testing.T, chainSrc string) {
	t.Helper()
	for _, derivation := range []string{
		"ConsensusEvidenceMaxAgeNumBlocks     int64 = archivetypes.DefaultBlocksIn14Days",
		"ConsensusEvidenceMaxAgeDurationNanos int64 = ConsensusEvidenceMaxAgeNumBlocks * params.BlockIntervalSeconds * int64(time.Second)",
	} {
		if !strings.Contains(chainSrc, derivation) {
			t.Fatalf("chain/app/consensus_params.go no longer derives the evidence window as %q: update setup/create_genesis.go", derivation)
		}
	}
	params, err := os.ReadFile("../../../../testdata/repo/chain/app/params/params.go")
	if err != nil {
		t.Fatalf("read the chain's params: %v", err)
	}
	m := regexp.MustCompile(`(?m)^\s*BlockIntervalSeconds = (\d+)\s*$`).FindSubmatch(params)
	if m == nil {
		t.Fatal("chain/app/params/params.go no longer declares BlockIntervalSeconds: update the evidence window")
	}
	interval, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil || interval <= 0 {
		t.Fatalf("BlockIntervalSeconds = %q is not a positive integer", m[1])
	}
	const retentionDays, secondsPerDay = 14, 24 * 60 * 60
	blocks := retentionDays * secondsPerDay / interval
	if got := strconv.FormatInt(blocks, 10); got != evidenceMaxAgeNumBlocks {
		t.Errorf("evidenceMaxAgeNumBlocks = %s, want %s (14 days of %d s blocks)", evidenceMaxAgeNumBlocks, got, interval)
	}
	if got := strconv.FormatInt(blocks*interval*int64(time.Second), 10); got != evidenceMaxAgeDurationNanos {
		t.Errorf("evidenceMaxAgeDurationNanos = %s, want %s", evidenceMaxAgeDurationNanos, got)
	}
}

func TestEvidenceWindow_staysUnderTheUnbondingTime(t *testing.T) {
	const unbonding = 21 * 24 * time.Hour
	nanos, err := strconv.ParseInt(evidenceMaxAgeDurationNanos, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(nanos) >= unbonding {
		t.Errorf("evidence max age %v must be under the unbonding time %v", time.Duration(nanos), unbonding)
	}
}
