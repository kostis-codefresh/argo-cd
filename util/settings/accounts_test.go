package settings

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/argoproj/argo-cd/v3/common"
)

func TestGetAccounts_NoAccountsConfigured(t *testing.T) {
	_, settingsManager := fixtures(nil)
	accounts, err := settingsManager.GetAccounts()
	require.NoError(t, err)

	adminAccount, ok := accounts[common.ArgoCDAdminUsername]
	assert.True(t, ok)
	assert.Equal(t, []AccountCapability{AccountCapabilityLogin}, adminAccount.Capabilities)
}

func TestGetAccounts_HasConfiguredAccounts(t *testing.T) {
	_, settingsManager := fixtures(map[string]string{"accounts.test": "apiKey"}, func(secret *corev1.Secret) {
		secret.Data["accounts.test.tokens"] = []byte(`[{"id":"123","iat":1583789194,"exp":1583789194}]`)
	})
	accounts, err := settingsManager.GetAccounts()
	require.NoError(t, err)

	acc, ok := accounts["test"]
	assert.True(t, ok)
	assert.ElementsMatch(t, []AccountCapability{AccountCapabilityApiKey}, acc.Capabilities)
	assert.ElementsMatch(t, []Token{{ID: "123", IssuedAt: 1583789194, ExpiresAt: 1583789194}}, acc.Tokens)
	assert.True(t, acc.Enabled)
}

func TestGetAccounts_DisableAccount(t *testing.T) {
	_, settingsManager := fixtures(map[string]string{
		"accounts.test":         "apiKey",
		"accounts.test.enabled": "false",
	})
	accounts, err := settingsManager.GetAccounts()
	require.NoError(t, err)

	acc, ok := accounts["test"]
	assert.True(t, ok)
	assert.False(t, acc.Enabled)
}

func TestGetAccount(t *testing.T) {
	_, settingsManager := fixtures(map[string]string{
		"accounts.test": "apiKey",
	})

	t.Run("ExistingUserName", func(t *testing.T) {
		_, err := settingsManager.GetAccount("test")

		require.NoError(t, err)
	})

	t.Run("IncorrectName", func(t *testing.T) {
		_, err := settingsManager.GetAccount("incorrect-name")

		require.Error(t, err)
		assert.Equal(t, codes.NotFound, status.Code(err))
	})
}

func TestGetAccount_WithInvalidToken(t *testing.T) {
	_, settingsManager := fixtures(map[string]string{
		"accounts.user1":       "apiKey",
		"accounts.invaliduser": "apiKey",
		"accounts.user2":       "apiKey",
	},
		func(secret *corev1.Secret) {
			secret.Data["accounts.user1.tokens"] = []byte(`[{"id":"1","iat":158378932,"exp":1583789194}]`)
		},
		func(secret *corev1.Secret) {
			secret.Data["accounts.invaliduser.tokens"] = []byte("Invalid token")
		},
		func(secret *corev1.Secret) {
			secret.Data["accounts.user2.tokens"] = []byte(`[{"id":"2","iat":1583789194,"exp":1583784545}]`)
		},
	)

	_, err := settingsManager.GetAccounts()
	require.NoError(t, err)
}

func TestGetAdminAccount(t *testing.T) {
	mTime := time.Now().Format(time.RFC3339)
	_, settingsManager := fixtures(nil, func(secret *corev1.Secret) {
		secret.Data["admin.password"] = []byte("admin-password")
		secret.Data["admin.passwordMtime"] = []byte(mTime)
	})

	acc, err := settingsManager.GetAccount(common.ArgoCDAdminUsername)
	require.NoError(t, err)

	assert.Equal(t, "admin-password", acc.PasswordHash)
	assert.Equal(t, mTime, acc.FormatPasswordMtime())
}

func TestFormatPasswordMtime_SuccessfullyFormatted(t *testing.T) {
	mTime := time.Now()
	acc := Account{PasswordMtime: &mTime}
	assert.Equal(t, mTime.Format(time.RFC3339), acc.FormatPasswordMtime())
}

func TestFormatPasswordMtime_NoMtime(t *testing.T) {
	acc := Account{}
	assert.Empty(t, acc.FormatPasswordMtime())
}

func TestHasCapability(t *testing.T) {
	acc := Account{Capabilities: []AccountCapability{AccountCapabilityApiKey}}
	assert.True(t, acc.HasCapability(AccountCapabilityApiKey))
	assert.False(t, acc.HasCapability(AccountCapabilityLogin))
}

func TestFormatCapabilities(t *testing.T) {
	acc := Account{Capabilities: []AccountCapability{AccountCapabilityLogin, AccountCapabilityApiKey}}
	assert.Equal(t, "login,apiKey", acc.FormatCapabilities())
}

func TestTokenIndex_TokenExists(t *testing.T) {
	acc := Account{Tokens: []Token{{ID: "123"}, {ID: "456"}}}
	index := acc.TokenIndex("456")
	assert.Equal(t, 1, index)
}

func TestTokenIndex_TokenDoesNotExist(t *testing.T) {
	acc := Account{Tokens: []Token{{ID: "123"}}}
	index := acc.TokenIndex("456")
	assert.Equal(t, -1, index)
}

func TestAddAccount_AccountAdded(t *testing.T) {
	clientset, settingsManager := fixtures(nil)
	mTime := time.Now()
	addedAccount := Account{
		Tokens:        []Token{{ID: "123"}},
		Capabilities:  []AccountCapability{AccountCapabilityLogin},
		Enabled:       false,
		PasswordHash:  "hash",
		PasswordMtime: &mTime,
	}
	err := settingsManager.AddAccount("test", addedAccount)
	require.NoError(t, err)

	cm, err := clientset.CoreV1().ConfigMaps("default").Get(t.Context(), common.ArgoCDConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, "login", cm.Data["accounts.test"])
	assert.Equal(t, "false", cm.Data["accounts.test.enabled"])

	secret, err := clientset.CoreV1().Secrets("default").Get(t.Context(), common.ArgoCDSecretName, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, "hash", string(secret.Data["accounts.test.password"]))
	assert.Equal(t, mTime.Format(time.RFC3339), string(secret.Data["accounts.test.passwordMtime"]))
	assert.JSONEq(t, `[{"id":"123","iat":0}]`, string(secret.Data["accounts.test.tokens"]))
}

func TestAddAccount_AlreadyExists(t *testing.T) {
	_, settingsManager := fixtures(map[string]string{"accounts.test": "login"})
	err := settingsManager.AddAccount("test", Account{})
	require.Error(t, err)
}

func TestAddAccount_CannotAddAdmin(t *testing.T) {
	_, settingsManager := fixtures(nil)
	err := settingsManager.AddAccount("admin", Account{})
	require.Error(t, err)
}

func TestUpdateAccount_SuccessfullyUpdated(t *testing.T) {
	clientset, settingsManager := fixtures(map[string]string{"accounts.test": "login"})
	mTime := time.Now()

	err := settingsManager.UpdateAccount("test", func(account *Account) error {
		account.Tokens = []Token{{ID: "123"}}
		account.Capabilities = []AccountCapability{AccountCapabilityLogin}
		account.Enabled = false
		account.PasswordHash = "hash"
		account.PasswordMtime = &mTime
		return nil
	})
	require.NoError(t, err)

	cm, err := clientset.CoreV1().ConfigMaps("default").Get(t.Context(), common.ArgoCDConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, "login", cm.Data["accounts.test"])
	assert.Equal(t, "false", cm.Data["accounts.test.enabled"])

	secret, err := clientset.CoreV1().Secrets("default").Get(t.Context(), common.ArgoCDSecretName, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, "hash", string(secret.Data["accounts.test.password"]))
	assert.Equal(t, mTime.Format(time.RFC3339), string(secret.Data["accounts.test.passwordMtime"]))
	assert.JSONEq(t, `[{"id":"123","iat":0}]`, string(secret.Data["accounts.test.tokens"]))
}

func TestUpdateAccount_UpdateAdminPassword(t *testing.T) {
	clientset, settingsManager := fixtures(nil)
	mTime := time.Now()

	err := settingsManager.UpdateAccount("admin", func(account *Account) error {
		account.PasswordHash = "newPassword"
		account.PasswordMtime = &mTime
		return nil
	})
	require.NoError(t, err)

	secret, err := clientset.CoreV1().Secrets("default").Get(t.Context(), common.ArgoCDSecretName, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, "newPassword", string(secret.Data["admin.password"]))
	assert.Equal(t, mTime.Format(time.RFC3339), string(secret.Data["admin.passwordMtime"]))
}

func TestUpdateAccount_AccountDoesNotExist(t *testing.T) {
	_, settingsManager := fixtures(map[string]string{"accounts.test": "login"})

	err := settingsManager.UpdateAccount("test1", func(account *Account) error {
		account.Enabled = false
		return nil
	})
	require.Error(t, err)
}
