//go:build dbtest

package tenancy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"stonesuite-backend/mfa"
)

// seedMFAIdentity creates a fresh identity to hang challenges off.
func seedMFAIdentity(t *testing.T, cp *ControlPlane) *Identity {
	t.Helper()
	tenantID := seedTestTenant(t, cp)
	identity, err := cp.CreateIdentity(context.Background(), tenantID,
		fmt.Sprintf("mfa-%d@example.com", time.Now().UnixNano()), "hash", "MFA User", true)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	return identity
}

// openTestChallenge opens a challenge with a unique token and returns it with
// the raw code it was hashed from.
func openTestChallenge(t *testing.T, cp *ControlPlane, identityID, purpose string) (*MFAChallenge, string) {
	t.Helper()
	code, err := mfa.GenerateCode()
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	codeHash, err := mfa.HashCode(code)
	if err != nil {
		t.Fatalf("HashCode: %v", err)
	}
	token := mfa.HashChallengeToken(fmt.Sprintf("token-%d", time.Now().UnixNano()))
	ch, err := cp.CreateMFAChallenge(context.Background(), identityID, purpose, token, codeHash, true)
	if err != nil {
		t.Fatalf("CreateMFAChallenge: %v", err)
	}
	return ch, code
}

// backdateLastSent moves a challenge's last send past the resend cooldown.
func backdateLastSent(t *testing.T, cp *ControlPlane, challengeID string) {
	t.Helper()
	if _, err := cp.pool.Exec(context.Background(),
		`UPDATE mfa_challenges SET last_sent_at = NOW() - INTERVAL '2 minutes' WHERE id = $1`, challengeID); err != nil {
		t.Fatalf("backdate last_sent_at: %v", err)
	}
}

func TestIdentityMFAStates(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)
	suffix := time.Now().UnixNano()

	withPassword, err := cp.CreateIdentity(ctx, tenantID, fmt.Sprintf("states-pw-%d@example.com", suffix), "hash", "Has Password", true)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	noPassword, err := cp.CreateIdentity(ctx, tenantID, fmt.Sprintf("states-sso-%d@example.com", suffix), "", "No Password", true)
	if err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	elsewhere := seedMFAIdentity(t, cp) // another tenant
	if err := cp.SetIdentityMFAEnabled(ctx, withPassword.ID, true); err != nil {
		t.Fatalf("SetIdentityMFAEnabled: %v", err)
	}

	states, err := cp.IdentityMFAStates(ctx, tenantID, []string{withPassword.ID, noPassword.ID, elsewhere.ID})
	if err != nil {
		t.Fatalf("IdentityMFAStates: %v", err)
	}
	if got := states[withPassword.ID]; !got.Enabled || !got.Available {
		t.Errorf("password identity = %+v, want enabled and available", got)
	}
	if got := states[noPassword.ID]; got.Enabled || got.Available {
		t.Errorf("password-less identity = %+v, want off and unavailable", got)
	}
	if _, found := states[elsewhere.ID]; found {
		t.Error("an identity from another tenant must not be reported")
	}

	empty, err := cp.IdentityMFAStates(ctx, tenantID, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("no ids = %v, %v; want empty and no error", empty, err)
	}
}

func TestSetIdentityMFAEnabled(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	identity := seedMFAIdentity(t, cp)
	if identity.MFAEnabled {
		t.Fatal("new identity must start with MFA off")
	}

	for _, want := range []bool{true, false} {
		if err := cp.SetIdentityMFAEnabled(ctx, identity.ID, want); err != nil {
			t.Fatalf("SetIdentityMFAEnabled(%v): %v", want, err)
		}
		got, err := cp.IdentityByID(ctx, identity.ID)
		if err != nil {
			t.Fatalf("IdentityByID: %v", err)
		}
		if got.MFAEnabled != want {
			t.Errorf("MFAEnabled = %v, want %v", got.MFAEnabled, want)
		}
	}

	const missingID = "00000000-0000-0000-0000-000000000000"
	if err := cp.SetIdentityMFAEnabled(ctx, missingID, true); !errors.Is(err, ErrIdentityNotFound) {
		t.Errorf("unknown identity: err = %v, want ErrIdentityNotFound", err)
	}
}

func TestMFAChallenge_LookupAndConsume(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	identity := seedMFAIdentity(t, cp)

	tokenHash := mfa.HashChallengeToken("lookup-" + identity.ID)
	codeHash, err := mfa.HashCode("123456")
	if err != nil {
		t.Fatalf("HashCode: %v", err)
	}
	created, err := cp.CreateMFAChallenge(ctx, identity.ID, mfa.PurposeLogin, tokenHash, codeHash, true)
	if err != nil {
		t.Fatalf("CreateMFAChallenge: %v", err)
	}
	if !created.RememberMe || created.SendCount != 1 || created.Attempts != 0 {
		t.Errorf("created = %+v, want remember_me, 1 send, 0 attempts", created)
	}

	got, err := cp.MFAChallengeByToken(ctx, tokenHash, mfa.PurposeLogin)
	if err != nil {
		t.Fatalf("MFAChallengeByToken: %v", err)
	}
	if got.ID != created.ID || !mfa.CodeMatches(got.CodeHash, "123456") {
		t.Errorf("lookup returned %+v, want challenge %s with the stored code", got, created.ID)
	}
	if _, err := cp.MFAChallengeByToken(ctx, tokenHash, mfa.PurposeEnable); !errors.Is(err, ErrMFAChallengeNotFound) {
		t.Errorf("wrong purpose: err = %v, want ErrMFAChallengeNotFound", err)
	}

	if err := cp.ConsumeMFAChallenge(ctx, created.ID); err != nil {
		t.Fatalf("ConsumeMFAChallenge: %v", err)
	}
	if err := cp.ConsumeMFAChallenge(ctx, created.ID); !errors.Is(err, ErrMFAChallengeNotFound) {
		t.Errorf("second consume: err = %v, want ErrMFAChallengeNotFound", err)
	}
	if _, err := cp.MFAChallengeByToken(ctx, tokenHash, mfa.PurposeLogin); !errors.Is(err, ErrMFAChallengeNotFound) {
		t.Errorf("consumed lookup: err = %v, want ErrMFAChallengeNotFound", err)
	}
}

func TestMFAChallenge_NewChallengeRetiresOldOfSamePurpose(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	identity := seedMFAIdentity(t, cp)

	firstLogin, _ := openTestChallenge(t, cp, identity.ID, mfa.PurposeLogin)
	enable, _ := openTestChallenge(t, cp, identity.ID, mfa.PurposeEnable)
	secondLogin, _ := openTestChallenge(t, cp, identity.ID, mfa.PurposeLogin)

	if err := cp.ConsumeMFAChallenge(ctx, firstLogin.ID); !errors.Is(err, ErrMFAChallengeNotFound) {
		t.Errorf("replaced login challenge still live: err = %v", err)
	}
	latest, err := cp.LatestMFAChallenge(ctx, identity.ID, mfa.PurposeLogin)
	if err != nil {
		t.Fatalf("LatestMFAChallenge(login): %v", err)
	}
	if latest.ID != secondLogin.ID {
		t.Errorf("latest login = %s, want %s", latest.ID, secondLogin.ID)
	}
	latestEnable, err := cp.LatestMFAChallenge(ctx, identity.ID, mfa.PurposeEnable)
	if err != nil {
		t.Fatalf("LatestMFAChallenge(enable): %v", err)
	}
	if latestEnable.ID != enable.ID {
		t.Errorf("a login challenge must not retire the enable one: got %s, want %s", latestEnable.ID, enable.ID)
	}
}

func TestMFAChallenge_WrongCodesLockItOut(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	identity := seedMFAIdentity(t, cp)
	ch, _ := openTestChallenge(t, cp, identity.ID, mfa.PurposeLogin)

	for want := 1; want <= mfa.MaxAttempts; want++ {
		got, err := cp.RecordMFAFailedAttempt(ctx, ch.ID)
		if err != nil {
			t.Fatalf("attempt %d: %v", want, err)
		}
		if got != want {
			t.Errorf("attempts = %d, want %d", got, want)
		}
	}
	if _, err := cp.RecordMFAFailedAttempt(ctx, ch.ID); !errors.Is(err, ErrMFAChallengeNotFound) {
		t.Errorf("attempt after lockout: err = %v, want ErrMFAChallengeNotFound", err)
	}
	if err := cp.ConsumeMFAChallenge(ctx, ch.ID); !errors.Is(err, ErrMFAChallengeNotFound) {
		t.Errorf("a locked-out challenge must not be consumable: err = %v", err)
	}
}

func TestMFAChallenge_Expired(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	identity := seedMFAIdentity(t, cp)
	ch, _ := openTestChallenge(t, cp, identity.ID, mfa.PurposeLogin)

	if _, err := cp.pool.Exec(ctx,
		`UPDATE mfa_challenges SET expires_at = NOW() - INTERVAL '1 second' WHERE id = $1`, ch.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, err := cp.LatestMFAChallenge(ctx, identity.ID, mfa.PurposeLogin); !errors.Is(err, ErrMFAChallengeNotFound) {
		t.Errorf("expired lookup: err = %v, want ErrMFAChallengeNotFound", err)
	}
	if err := cp.ConsumeMFAChallenge(ctx, ch.ID); !errors.Is(err, ErrMFAChallengeNotFound) {
		t.Errorf("expired consume: err = %v, want ErrMFAChallengeNotFound", err)
	}
	if _, err := cp.ResendMFAChallenge(ctx, ch.ID, "hash"); !errors.Is(err, ErrMFAChallengeNotFound) {
		t.Errorf("expired resend: err = %v, want ErrMFAChallengeNotFound", err)
	}
}

func TestMFAChallenge_Resend(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	identity := seedMFAIdentity(t, cp)
	ch, oldCode := openTestChallenge(t, cp, identity.ID, mfa.PurposeLogin)

	if _, err := cp.ResendMFAChallenge(ctx, ch.ID, "hash"); !errors.Is(err, ErrMFAResendRefused) {
		t.Fatalf("resend inside cooldown: err = %v, want ErrMFAResendRefused", err)
	}

	if _, err := cp.RecordMFAFailedAttempt(ctx, ch.ID); err != nil {
		t.Fatalf("RecordMFAFailedAttempt: %v", err)
	}
	backdateLastSent(t, cp, ch.ID)
	newHash, err := mfa.HashCode("654321")
	if err != nil {
		t.Fatalf("HashCode: %v", err)
	}
	resent, err := cp.ResendMFAChallenge(ctx, ch.ID, newHash)
	if err != nil {
		t.Fatalf("ResendMFAChallenge: %v", err)
	}
	if resent.SendCount != 2 {
		t.Errorf("send_count = %d, want 2", resent.SendCount)
	}
	if resent.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (a resend does not reset wrong-code attempts)", resent.Attempts)
	}
	if mfa.CodeMatches(resent.CodeHash, oldCode) || !mfa.CodeMatches(resent.CodeHash, "654321") {
		t.Error("resend must replace the code")
	}

	for sends := resent.SendCount; sends < mfa.MaxSendsPerChallenge; sends++ {
		backdateLastSent(t, cp, ch.ID)
		if _, err := cp.ResendMFAChallenge(ctx, ch.ID, newHash); err != nil {
			t.Fatalf("resend %d: %v", sends+1, err)
		}
	}
	backdateLastSent(t, cp, ch.ID)
	if _, err := cp.ResendMFAChallenge(ctx, ch.ID, newHash); !errors.Is(err, ErrMFAResendRefused) {
		t.Errorf("resend past the per-challenge cap: err = %v, want ErrMFAResendRefused", err)
	}
}

func TestMFAChallenge_SendWindowCap(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	identity := seedMFAIdentity(t, cp)

	for i := 0; i < mfa.MaxSendsPerWindow; i++ {
		openTestChallenge(t, cp, identity.ID, mfa.PurposeLogin)
	}
	codeHash, err := mfa.HashCode("000000")
	if err != nil {
		t.Fatalf("HashCode: %v", err)
	}
	_, err = cp.CreateMFAChallenge(ctx, identity.ID, mfa.PurposeEnable,
		mfa.HashChallengeToken("over-cap-"+identity.ID), codeHash, false)
	if !errors.Is(err, ErrMFASendLimit) {
		t.Errorf("challenge past the window cap: err = %v, want ErrMFASendLimit", err)
	}

	// Sends older than the window no longer count.
	if _, err := cp.pool.Exec(ctx,
		`UPDATE mfa_challenges SET created_at = NOW() - INTERVAL '2 hours' WHERE identity_id = $1`, identity.ID); err != nil {
		t.Fatalf("age challenges: %v", err)
	}
	if _, err := cp.CreateMFAChallenge(ctx, identity.ID, mfa.PurposeEnable,
		mfa.HashChallengeToken("after-window-"+identity.ID), codeHash, false); err != nil {
		t.Errorf("challenge after the window passed: %v", err)
	}
}

func TestMFAChallenge_UnknownIdentity(t *testing.T) {
	cp := newCPTestControlPlane(t)
	const missingID = "00000000-0000-0000-0000-000000000000"
	_, err := cp.CreateMFAChallenge(context.Background(), missingID, mfa.PurposeLogin, "token", "hash", false)
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Errorf("err = %v, want ErrIdentityNotFound", err)
	}
}
