package model

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/performance_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	PaymentConfigurationVersionOptionKey = "payment_setting.config_version"
	initialPaymentConfigurationVersion   = int64(1)
)

var (
	ErrPaymentConfigurationVersionConflict = errors.New("payment configuration version conflict")
	ErrPaymentConfigurationPrecondition    = errors.New("payment configuration precondition failed")
)
var errPaymentOptionsChangedDuringReload = errors.New("payment options changed during reload")

type Option struct {
	Key   string `json:"key" gorm:"primaryKey"`
	Value string `json:"value"`
}

func optionKeyColumn() string {
	if commonKeyCol != "" {
		return commonKeyCol
	}
	if DB != nil && DB.Dialector != nil && DB.Dialector.Name() == "postgres" {
		return `"key"`
	}
	return "`key`"
}

func AllOption() ([]*Option, error) {
	var options []*Option
	var err error
	err = DB.Find(&options).Error
	return options, err
}

func DeleteOptions(keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	return DB.Where(fmt.Sprintf("%s IN ?", optionKeyColumn()), keys).Delete(&Option{}).Error
}

func InitOptionMap() {
	common.OptionMapRWMutex.Lock()
	common.OptionMap = make(map[string]string)

	// 添加原有的系统配置
	common.OptionMap["FileUploadPermission"] = strconv.Itoa(common.FileUploadPermission)
	common.OptionMap["FileDownloadPermission"] = strconv.Itoa(common.FileDownloadPermission)
	common.OptionMap["ImageUploadPermission"] = strconv.Itoa(common.ImageUploadPermission)
	common.OptionMap["ImageDownloadPermission"] = strconv.Itoa(common.ImageDownloadPermission)
	common.OptionMap["PasswordLoginEnabled"] = strconv.FormatBool(common.PasswordLoginEnabled)
	common.OptionMap["PasswordRegisterEnabled"] = strconv.FormatBool(common.PasswordRegisterEnabled)
	common.OptionMap["EmailVerificationEnabled"] = strconv.FormatBool(common.EmailVerificationEnabled)
	common.OptionMap["GitHubOAuthEnabled"] = strconv.FormatBool(common.GitHubOAuthEnabled)
	common.OptionMap["LinuxDOOAuthEnabled"] = strconv.FormatBool(common.LinuxDOOAuthEnabled)
	common.OptionMap["TelegramOAuthEnabled"] = strconv.FormatBool(common.TelegramOAuthEnabled)
	common.OptionMap["WeChatAuthEnabled"] = strconv.FormatBool(common.WeChatAuthEnabled)
	common.OptionMap["TurnstileCheckEnabled"] = strconv.FormatBool(common.TurnstileCheckEnabled)
	common.OptionMap["RegisterEnabled"] = strconv.FormatBool(common.RegisterEnabled)
	common.OptionMap["AutomaticDisableChannelEnabled"] = strconv.FormatBool(common.AutomaticDisableChannelEnabled)
	common.OptionMap["AutomaticEnableChannelEnabled"] = strconv.FormatBool(common.AutomaticEnableChannelEnabled)
	common.OptionMap["LogConsumeEnabled"] = strconv.FormatBool(common.LogConsumeEnabled)
	common.OptionMap["DisplayInCurrencyEnabled"] = strconv.FormatBool(common.DisplayInCurrencyEnabled)
	common.OptionMap["DisplayTokenStatEnabled"] = strconv.FormatBool(common.DisplayTokenStatEnabled)
	common.OptionMap["DrawingEnabled"] = strconv.FormatBool(common.DrawingEnabled)
	common.OptionMap["TaskEnabled"] = strconv.FormatBool(common.TaskEnabled)
	common.OptionMap["DataExportEnabled"] = strconv.FormatBool(common.DataExportEnabled)
	common.OptionMap["ChannelDisableThreshold"] = strconv.FormatFloat(common.ChannelDisableThreshold, 'f', -1, 64)
	common.OptionMap["EmailDomainRestrictionEnabled"] = strconv.FormatBool(common.EmailDomainRestrictionEnabled)
	common.OptionMap["EmailAliasRestrictionEnabled"] = strconv.FormatBool(common.EmailAliasRestrictionEnabled)
	common.OptionMap["EmailDomainWhitelist"] = strings.Join(common.EmailDomainWhitelist, ",")
	common.OptionMap["SMTPServer"] = ""
	common.OptionMap["SMTPFrom"] = ""
	common.OptionMap["SMTPPort"] = strconv.Itoa(common.SMTPPort)
	common.OptionMap["SMTPAccount"] = ""
	common.OptionMap["SMTPToken"] = ""
	common.OptionMap["SMTPSSLEnabled"] = strconv.FormatBool(common.SMTPSSLEnabled)
	common.OptionMap["SMTPStartTLSEnabled"] = strconv.FormatBool(common.SMTPStartTLSEnabled)
	common.OptionMap["SMTPInsecureSkipVerify"] = strconv.FormatBool(common.SMTPInsecureSkipVerify)
	common.OptionMap["SMTPForceAuthLogin"] = strconv.FormatBool(common.SMTPForceAuthLogin)
	common.OptionMap["Notice"] = ""
	common.OptionMap["About"] = ""
	common.OptionMap["HomePageContent"] = ""
	common.OptionMap["Footer"] = common.Footer
	common.OptionMap["SystemName"] = common.SystemName
	common.OptionMap["Logo"] = common.Logo
	common.OptionMap["ServerAddress"] = ""
	common.OptionMap["WorkerUrl"] = system_setting.WorkerUrl
	common.OptionMap["WorkerValidKey"] = system_setting.WorkerValidKey
	common.OptionMap["WorkerAllowHttpImageRequestEnabled"] = strconv.FormatBool(system_setting.WorkerAllowHttpImageRequestEnabled)
	common.OptionMap["PayAddress"] = ""
	common.OptionMap["CustomCallbackAddress"] = ""
	common.OptionMap["EpayId"] = ""
	common.OptionMap["EpayKey"] = ""
	common.OptionMap["EpayCurrency"] = operation_setting.EpayCurrency
	common.OptionMap["EpayCredentialGeneration"] = strconv.FormatInt(operation_setting.EpayCredentialGeneration, 10)
	common.OptionMap["EpayIdPrevious"] = operation_setting.EpayIdPrevious
	common.OptionMap["EpayKeyPrevious"] = operation_setting.EpayKeyPrevious
	common.OptionMap["EpayPreviousCredentialGeneration"] = strconv.FormatInt(operation_setting.EpayPreviousCredentialGeneration, 10)
	common.OptionMap["EpayPreviousValidBefore"] = strconv.FormatInt(operation_setting.EpayPreviousValidBefore, 10)
	common.OptionMap["EpayPreviousExpiresAt"] = strconv.FormatInt(operation_setting.EpayPreviousExpiresAt, 10)
	common.OptionMap["Price"] = strconv.FormatFloat(operation_setting.Price, 'f', -1, 64)
	common.OptionMap["USDExchangeRate"] = strconv.FormatFloat(operation_setting.USDExchangeRate, 'f', -1, 64)
	common.OptionMap["MinTopUp"] = strconv.Itoa(operation_setting.MinTopUp)
	common.OptionMap["StripeMinTopUp"] = strconv.Itoa(setting.StripeMinTopUp)
	common.OptionMap["StripeApiSecret"] = setting.StripeApiSecret
	common.OptionMap["StripeWebhookSecret"] = setting.StripeWebhookSecret
	common.OptionMap["StripeWebhookSecretPrevious"] = setting.StripeWebhookSecretPrevious
	common.OptionMap["StripeWebhookSecretPreviousExpiresAt"] = strconv.FormatInt(setting.StripeWebhookSecretPreviousExpiresAt, 10)
	common.OptionMap["StripeWebhookCredentialGeneration"] = strconv.FormatInt(setting.StripeWebhookCredentialGeneration, 10)
	common.OptionMap["StripeWebhookPreviousCredentialGeneration"] = strconv.FormatInt(setting.StripeWebhookPreviousCredentialGeneration, 10)
	common.OptionMap["StripeWebhookPreviousValidBefore"] = strconv.FormatInt(setting.StripeWebhookPreviousValidBefore, 10)
	common.OptionMap["StripePriceId"] = setting.StripePriceId
	common.OptionMap["StripeCurrency"] = setting.StripeCurrency
	common.OptionMap["StripeAccountId"] = setting.StripeAccountId
	common.OptionMap["StripeCheckoutAllowedHosts"] = setting.StripeCheckoutAllowedHosts
	common.OptionMap["StripeCredentialAccountId"] = setting.StripeCredentialAccountId
	common.OptionMap["StripeCredentialLivemode"] = setting.StripeCredentialLivemode
	common.OptionMap["StripeWebhookCredentialLivemode"] = setting.StripeWebhookCredentialLivemode
	common.OptionMap["StripeConfigurationVerifiedFingerprint"] = setting.StripeConfigurationVerifiedFingerprint
	common.OptionMap["StripeConfigurationVerifiedAt"] = strconv.FormatInt(setting.StripeConfigurationVerifiedAt, 10)
	common.OptionMap["StripeUnitPrice"] = strconv.FormatFloat(setting.StripeUnitPrice, 'f', -1, 64)
	common.OptionMap["StripePromotionCodesEnabled"] = strconv.FormatBool(setting.StripePromotionCodesEnabled)
	common.OptionMap["XorPayAid"] = setting.XorPayAid
	common.OptionMap["XorPayAppSecret"] = setting.XorPayAppSecret
	common.OptionMap["XorPayCredentialGeneration"] = strconv.FormatInt(setting.XorPayCredentialGeneration, 10)
	common.OptionMap["XorPayAidPrevious"] = setting.XorPayAidPrevious
	common.OptionMap["XorPayAppSecretPrevious"] = setting.XorPayAppSecretPrevious
	common.OptionMap["XorPayPreviousCredentialGeneration"] = strconv.FormatInt(setting.XorPayPreviousCredentialGeneration, 10)
	common.OptionMap["XorPayPreviousValidBefore"] = strconv.FormatInt(setting.XorPayPreviousValidBefore, 10)
	common.OptionMap["XorPayPreviousExpiresAt"] = strconv.FormatInt(setting.XorPayPreviousExpiresAt, 10)
	common.OptionMap["XorPayUnitPrice"] = strconv.FormatFloat(setting.XorPayUnitPrice, 'f', -1, 64)
	common.OptionMap["XorPayMinTopUp"] = strconv.Itoa(setting.XorPayMinTopUp)
	common.OptionMap["XorPayCurrency"] = setting.XorPayCurrency
	common.OptionMap["XorPayEnabledMethods"] = setting.XorPayEnabledMethods2JsonString()
	common.OptionMap[PaymentConfigurationVersionOptionKey] = strconv.FormatInt(initialPaymentConfigurationVersion, 10)
	common.OptionMap["CreemApiKey"] = setting.CreemApiKey
	common.OptionMap["CreemProducts"] = setting.CreemProducts
	common.OptionMap["CreemTestMode"] = strconv.FormatBool(setting.CreemTestMode)
	common.OptionMap["CreemWebhookSecret"] = setting.CreemWebhookSecret
	common.OptionMap["WaffoEnabled"] = strconv.FormatBool(setting.WaffoEnabled)
	common.OptionMap["WaffoApiKey"] = setting.WaffoApiKey
	common.OptionMap["WaffoPrivateKey"] = setting.WaffoPrivateKey
	common.OptionMap["WaffoPublicCert"] = setting.WaffoPublicCert
	common.OptionMap["WaffoSandboxPublicCert"] = setting.WaffoSandboxPublicCert
	common.OptionMap["WaffoSandboxApiKey"] = setting.WaffoSandboxApiKey
	common.OptionMap["WaffoSandboxPrivateKey"] = setting.WaffoSandboxPrivateKey
	common.OptionMap["WaffoSandbox"] = strconv.FormatBool(setting.WaffoSandbox)
	common.OptionMap["WaffoMerchantId"] = setting.WaffoMerchantId
	common.OptionMap["WaffoNotifyUrl"] = setting.WaffoNotifyUrl
	common.OptionMap["WaffoReturnUrl"] = setting.WaffoReturnUrl
	common.OptionMap["WaffoSubscriptionReturnUrl"] = setting.WaffoSubscriptionReturnUrl
	common.OptionMap["WaffoWebRedirectHosts"] = setting.WaffoWebRedirectHosts
	common.OptionMap["WaffoAppRedirectSchemes"] = setting.WaffoAppRedirectSchemes
	common.OptionMap["WaffoCurrency"] = setting.WaffoCurrency
	common.OptionMap["WaffoUnitPrice"] = strconv.FormatFloat(setting.WaffoUnitPrice, 'f', -1, 64)
	common.OptionMap["WaffoMinTopUp"] = strconv.Itoa(setting.WaffoMinTopUp)
	common.OptionMap["WaffoPayMethods"] = setting.WaffoPayMethods2JsonString()
	common.OptionMap["WaffoPancakeMerchantID"] = setting.WaffoPancakeMerchantID
	common.OptionMap["WaffoPancakePrivateKey"] = setting.WaffoPancakePrivateKey
	common.OptionMap["WaffoPancakeReturnURL"] = setting.WaffoPancakeReturnURL
	common.OptionMap["WaffoPancakeTestMode"] = strconv.FormatBool(setting.WaffoPancakeTestMode)
	common.OptionMap["WaffoPancakeUnitPrice"] = strconv.FormatFloat(setting.WaffoPancakeUnitPrice, 'f', -1, 64)
	common.OptionMap["WaffoPancakeMinTopUp"] = strconv.Itoa(setting.WaffoPancakeMinTopUp)
	common.OptionMap["WaffoPancakeStoreID"] = setting.WaffoPancakeStoreID
	common.OptionMap["WaffoPancakeProductID"] = setting.WaffoPancakeProductID
	common.OptionMap["TopupGroupRatio"] = common.TopupGroupRatio2JSONString()
	common.OptionMap["Chats"] = setting.Chats2JsonString()
	common.OptionMap["AutoGroups"] = setting.AutoGroups2JsonString()
	common.OptionMap["DefaultUseAutoGroup"] = strconv.FormatBool(setting.DefaultUseAutoGroup)
	common.OptionMap["PayMethods"] = operation_setting.PayMethods2JsonString()
	common.OptionMap["GitHubClientId"] = ""
	common.OptionMap["GitHubClientSecret"] = ""
	common.OptionMap["TelegramBotToken"] = ""
	common.OptionMap["TelegramBotName"] = ""
	common.OptionMap["WeChatServerAddress"] = ""
	common.OptionMap["WeChatServerToken"] = ""
	common.OptionMap["WeChatAccountQRCodeImageURL"] = ""
	common.OptionMap["TurnstileSiteKey"] = ""
	common.OptionMap["TurnstileSecretKey"] = ""
	common.OptionMap["QuotaForNewUser"] = strconv.Itoa(common.QuotaForNewUser)
	common.OptionMap["QuotaForInviter"] = strconv.Itoa(common.QuotaForInviter)
	common.OptionMap["QuotaForInvitee"] = strconv.Itoa(common.QuotaForInvitee)
	common.OptionMap["QuotaRemindThreshold"] = strconv.Itoa(common.QuotaRemindThreshold)
	common.OptionMap["PreConsumedQuota"] = strconv.Itoa(common.PreConsumedQuota)
	common.OptionMap["ModelRequestRateLimitCount"] = strconv.Itoa(setting.ModelRequestRateLimitCount)
	common.OptionMap["ModelRequestRateLimitDurationMinutes"] = strconv.Itoa(setting.ModelRequestRateLimitDurationMinutes)
	common.OptionMap["ModelRequestRateLimitSuccessCount"] = strconv.Itoa(setting.ModelRequestRateLimitSuccessCount)
	common.OptionMap["ModelRequestRateLimitGroup"] = setting.ModelRequestRateLimitGroup2JSONString()
	common.OptionMap["ModelRatio"] = ratio_setting.ModelRatio2JSONString()
	common.OptionMap["ModelPrice"] = ratio_setting.ModelPrice2JSONString()
	common.OptionMap["CacheRatio"] = ratio_setting.CacheRatio2JSONString()
	common.OptionMap["CreateCacheRatio"] = ratio_setting.CreateCacheRatio2JSONString()
	common.OptionMap["GroupRatio"] = ratio_setting.GroupRatio2JSONString()
	common.OptionMap["GroupGroupRatio"] = ratio_setting.GroupGroupRatio2JSONString()
	common.OptionMap["UserUsableGroups"] = setting.UserUsableGroups2JSONString()
	common.OptionMap["CompletionRatio"] = ratio_setting.CompletionRatio2JSONString()
	common.OptionMap["ImageRatio"] = ratio_setting.ImageRatio2JSONString()
	common.OptionMap["AudioRatio"] = ratio_setting.AudioRatio2JSONString()
	common.OptionMap["AudioCompletionRatio"] = ratio_setting.AudioCompletionRatio2JSONString()
	common.OptionMap["TopUpLink"] = common.TopUpLink
	//common.OptionMap["ChatLink"] = common.ChatLink
	//common.OptionMap["ChatLink2"] = common.ChatLink2
	common.OptionMap["QuotaPerUnit"] = strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64)
	common.OptionMap["RetryTimes"] = strconv.Itoa(common.RetryTimes)
	common.OptionMap["DataExportInterval"] = strconv.Itoa(common.DataExportInterval)
	common.OptionMap["DataExportDefaultTime"] = common.DataExportDefaultTime
	common.OptionMap["DefaultCollapseSidebar"] = strconv.FormatBool(common.DefaultCollapseSidebar)
	common.OptionMap["MjNotifyEnabled"] = strconv.FormatBool(setting.MjNotifyEnabled)
	common.OptionMap["MjAccountFilterEnabled"] = strconv.FormatBool(setting.MjAccountFilterEnabled)
	common.OptionMap["MjModeClearEnabled"] = strconv.FormatBool(setting.MjModeClearEnabled)
	common.OptionMap["MjForwardUrlEnabled"] = strconv.FormatBool(setting.MjForwardUrlEnabled)
	common.OptionMap["MjActionCheckSuccessEnabled"] = strconv.FormatBool(setting.MjActionCheckSuccessEnabled)
	common.OptionMap["CheckSensitiveEnabled"] = strconv.FormatBool(setting.CheckSensitiveEnabled)
	common.OptionMap["DemoSiteEnabled"] = strconv.FormatBool(operation_setting.DemoSiteEnabled)
	common.OptionMap["SelfUseModeEnabled"] = strconv.FormatBool(operation_setting.SelfUseModeEnabled)
	common.OptionMap["ModelRequestRateLimitEnabled"] = strconv.FormatBool(setting.ModelRequestRateLimitEnabled)
	common.OptionMap["CheckSensitiveOnPromptEnabled"] = strconv.FormatBool(setting.CheckSensitiveOnPromptEnabled)
	common.OptionMap["StopOnSensitiveEnabled"] = strconv.FormatBool(setting.StopOnSensitiveEnabled)
	common.OptionMap["SensitiveWords"] = setting.SensitiveWordsToString()
	common.OptionMap["StreamCacheQueueLength"] = strconv.Itoa(setting.StreamCacheQueueLength)
	common.OptionMap["AutomaticDisableKeywords"] = operation_setting.AutomaticDisableKeywordsToString()
	common.OptionMap["AutomaticDisableStatusCodes"] = operation_setting.AutomaticDisableStatusCodesToString()
	common.OptionMap["AutomaticRetryStatusCodes"] = operation_setting.AutomaticRetryStatusCodesToString()
	common.OptionMap["ExposeRatioEnabled"] = strconv.FormatBool(ratio_setting.IsExposeRatioEnabled())

	// 自动添加所有注册的模型配置
	modelConfigs := config.GlobalConfig.ExportAllConfigs()
	for k, v := range modelConfigs {
		common.OptionMap[k] = v
	}

	common.OptionMapRWMutex.Unlock()
	loadOptionsFromDatabase()
}

func loadOptionsFromDatabase() {
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForUpdate()
	defer unlockPaymentConfiguration()
	for attempt := 0; attempt < 4; attempt++ {
		if err := loadOptionsFromDatabaseWithPaymentConfigurationLockHeld(); err != nil {
			if errors.Is(err, errPaymentOptionsChangedDuringReload) {
				continue
			}
			common.SysLog("failed to load options from database: " + err.Error())
			return
		}
		return
	}
	common.SysLog("failed to load options from database: options kept changing during reload")
}

func loadOptionsFromDatabaseWithPaymentConfigurationLockHeld() error {
	options, err := AllOption()
	if err != nil {
		return fmt.Errorf("load options from database: %w", err)
	}

	type loadedOption struct {
		key            string
		value          string
		storedValue    string
		rewrappedValue string
	}
	loaded := make([]loadedOption, 0, len(options)+1)
	versionFound := false
	hasRewrappedOption := false
	for _, option := range options {
		value, decryptErr := decryptPaymentOptionValue(option.Key, option.Value)
		if decryptErr != nil {
			return fmt.Errorf("decrypt payment option %s: %w", option.Key, decryptErr)
		}
		if option.Key == PaymentConfigurationVersionOptionKey {
			if _, err := parsePaymentConfigurationVersion(value, "stored"); err != nil {
				return err
			}
			versionFound = true
		}
		candidate := loadedOption{key: option.Key, value: value, storedValue: option.Value}
		if IsPaymentSecretOption(option.Key) && paymentOptionNeedsRewrap(option.Value) {
			encrypted, encryptErr := encryptPaymentOptionValue(option.Key, value)
			if encryptErr != nil {
				return fmt.Errorf("rewrap payment option %s: %w", option.Key, encryptErr)
			}
			if encrypted != option.Value {
				candidate.rewrappedValue = encrypted
				hasRewrappedOption = true
			}
		}
		loaded = append(loaded, candidate)
	}
	if !versionFound {
		loaded = append(loaded, loadedOption{
			key:   PaymentConfigurationVersionOptionKey,
			value: strconv.FormatInt(initialPaymentConfigurationVersion, 10),
		})
	}

	primary, hasPrimary := primaryPaymentSecretKey()
	paymentSecretStorageReadiness.RLock()
	startPayloadRefreshRequired := hasPrimary &&
		(paymentSecretStorageReadiness.keyID != primary.id || !paymentSecretStorageReadiness.ready)
	paymentSecretStorageReadiness.RUnlock()
	if hasRewrappedOption || startPayloadRefreshRequired {
		if err := DB.Transaction(func(tx *gorm.DB) error {
			for _, option := range loaded {
				if option.rewrappedValue == "" {
					continue
				}
				result := tx.Model(&Option{}).
					Where(fmt.Sprintf("%s = ? AND value = ?", optionKeyColumn()), option.key, option.storedValue).
					Update("value", option.rewrappedValue)
				if result.Error != nil {
					return fmt.Errorf("persist rewrapped payment option %s: %w", option.key, result.Error)
				}
				if result.RowsAffected != 1 {
					return fmt.Errorf("%w: %s", errPaymentOptionsChangedDuringReload, option.key)
				}
			}
			if startPayloadRefreshRequired {
				if err := rewrapPaymentOrderStartPayloadsTx(tx); err != nil {
					return err
				}
				return rewrapPaymentOrderBrowserAuthorizationsTx(tx)
			}
			return nil
		}); err != nil {
			return err
		}
	}

	// Apply the version marker last. If an option-specific parser rejects a
	// stored value, the stale local version remains visible and the next sync
	// retries instead of treating a partial refresh as current.
	sort.Slice(loaded, func(i, j int) bool {
		if loaded[i].key == PaymentConfigurationVersionOptionKey {
			return false
		}
		if loaded[j].key == PaymentConfigurationVersionOptionKey {
			return true
		}
		return loaded[i].key < loaded[j].key
	})
	for _, option := range loaded {
		if err := updateOptionMap(option.key, option.value); err != nil {
			return fmt.Errorf("refresh option %s: %w", option.key, err)
		}
	}
	if hasRewrappedOption || startPayloadRefreshRequired {
		return refreshPaymentSecretStorageReadiness()
	}
	return nil
}

func parsePaymentConfigurationVersion(value, source string) (int64, error) {
	version, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("invalid %s payment configuration version %q", source, value)
	}
	return version, nil
}

func paymentConfigurationVersionFromDatabase() (int64, error) {
	if DB == nil {
		return 0, errors.New("payment configuration database is not initialized")
	}
	var option Option
	result := DB.Select("value").Where(fmt.Sprintf("%s = ?", optionKeyColumn()), PaymentConfigurationVersionOptionKey).Limit(1).Find(&option)
	if result.Error != nil {
		return 0, fmt.Errorf("read payment configuration version: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return initialPaymentConfigurationVersion, nil
	}
	return parsePaymentConfigurationVersion(option.Value, "stored")
}

func paymentConfigurationVersionFromOptionMap() (int64, error) {
	common.OptionMapRWMutex.RLock()
	value, exists := common.OptionMap[PaymentConfigurationVersionOptionKey]
	common.OptionMapRWMutex.RUnlock()
	if !exists {
		return initialPaymentConfigurationVersion, nil
	}
	return parsePaymentConfigurationVersion(value, "local")
}

func CurrentPaymentConfigurationVersion() (int64, error) {
	return paymentConfigurationVersionFromOptionMap()
}

// SyncPaymentConfigurationIfStale refreshes the process-local option snapshot
// when another application instance has committed a newer configuration
// version. The second comparison under the process-wide write lock keeps
// concurrent callers from redundantly reloading or observing a partial local
// refresh.
func SyncPaymentConfigurationIfStale() error {
	databaseVersion, err := paymentConfigurationVersionFromDatabase()
	if err != nil {
		return err
	}
	localVersion, err := paymentConfigurationVersionFromOptionMap()
	if err != nil {
		return err
	}
	if databaseVersion == localVersion {
		return nil
	}

	unlockPaymentConfiguration := setting.LockPaymentConfigurationForUpdate()
	defer unlockPaymentConfiguration()

	for attempt := 0; attempt < 4; attempt++ {
		databaseVersion, err = paymentConfigurationVersionFromDatabase()
		if err != nil {
			return err
		}
		localVersion, err = paymentConfigurationVersionFromOptionMap()
		if err != nil {
			return err
		}
		if databaseVersion == localVersion {
			return nil
		}
		if err := loadOptionsFromDatabaseWithPaymentConfigurationLockHeld(); err != nil {
			if errors.Is(err, errPaymentOptionsChangedDuringReload) {
				continue
			}
			return err
		}
		databaseVersion, err = paymentConfigurationVersionFromDatabase()
		if err != nil {
			return err
		}
		localVersion, err = paymentConfigurationVersionFromOptionMap()
		if err != nil {
			return err
		}
		if databaseVersion == localVersion {
			return nil
		}
	}
	return errors.New("payment configuration kept changing during synchronization")
}

func SyncOptions(frequency int) {
	for {
		time.Sleep(time.Duration(frequency) * time.Second)
		common.SysLog("syncing options from database")
		loadOptionsFromDatabase()
	}
}

func validateOptionValueBeforePersistence(key, value string) error {
	if err := ratio_setting.ValidatePriceRatioOption(key, value); err != nil {
		return fmt.Errorf("invalid %s: %w", key, err)
	}
	if key == "tool_price_setting.prices" {
		if err := operation_setting.ValidateToolPricesByJSONString(value); err != nil {
			return fmt.Errorf("invalid %s: %w", key, err)
		}
	}
	if key == "StripeCheckoutAllowedHosts" {
		if _, err := setting.NormalizeStripeCheckoutAllowedHosts(value); err != nil {
			return err
		}
	}
	return nil
}

func paymentOptionStorageValue(key, value string) (string, error) {
	if key == "PayMethods" {
		methods, err := operation_setting.ParsePayMethodsByJsonString(value)
		if err != nil {
			return "", err
		}
		value, err = operation_setting.PayMethodsStorageJSON(methods)
		if err != nil {
			return "", err
		}
	}
	return encryptPaymentOptionValue(key, value)
}

func UpdateOption(key string, value string) error {
	if key == PaymentConfigurationVersionOptionKey {
		return errors.New("payment configuration version cannot be updated directly")
	}
	if err := validateOptionValueBeforePersistence(key, value); err != nil {
		return err
	}
	// Save to database first
	option := Option{
		Key: key,
	}
	// https://gorm.io/docs/update.html#Save-All-Fields
	if err := DB.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
		return err
	}
	storageValue, err := paymentOptionStorageValue(key, value)
	if err != nil {
		return err
	}
	option.Value = storageValue
	// Save is a combination function.
	// If save value does not contain primary key, it will execute Create,
	// otherwise it will execute Update (with all fields).
	if err := DB.Save(&option).Error; err != nil {
		return err
	}
	// Update OptionMap
	return updateOptionMap(key, value)
}

// UpdateOptionsBulk persists multiple key/value pairs in a single database
// transaction, then dispatches them through updateOptionMap in one pass. If
// any DB write fails the whole transaction rolls back and no in-memory state
// is touched — safe for callers that must commit a set of related options
// atomically (e.g. payment gateway binding).
func UpdateOptionsBulk(values map[string]string) error {
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForUpdate()
	defer unlockPaymentConfiguration()
	return updateOptionsBulk(values)
}

// UpdateOptionsBulkWithPaymentConfigurationLockHeld is used only by callers
// that must validate a provider snapshot and persist it under the same global
// payment-configuration write lock.
func UpdateOptionsBulkWithPaymentConfigurationLockHeld(values map[string]string) error {
	return updateOptionsBulk(values)
}

func updateOptionsBulk(values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	if _, exists := values[PaymentConfigurationVersionOptionKey]; exists {
		return errors.New("payment configuration version cannot be updated directly")
	}
	for key, value := range values {
		if err := validateOptionValueBeforePersistence(key, value); err != nil {
			return err
		}
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range values {
			option := Option{Key: k}
			if err := tx.FirstOrCreate(&option, Option{Key: k}).Error; err != nil {
				return err
			}
			storageValue, err := paymentOptionStorageValue(k, v)
			if err != nil {
				return err
			}
			option.Value = storageValue
			if err := tx.Save(&option).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for k, v := range values {
		if err := updateOptionMap(k, v); err != nil {
			return err
		}
	}
	return nil
}

// UpdatePaymentOptionsBulkWithVersionLockHeld atomically persists a payment
// configuration snapshot when expectedVersion matches the current durable
// version. The caller must hold the process-wide payment-configuration write
// lock so validation, persistence, and the in-memory refresh share one local
// critical section; the locked version row provides the equivalent guard
// across multiple application instances.
func UpdatePaymentOptionsBulkWithVersionLockHeld(values map[string]string, expectedVersion int64) (int64, error) {
	return updatePaymentOptionsWithVersionLockHeld(values, expectedVersion, nil, nil, nil)
}

type PaymentCredentialRevocation struct {
	Provider        string
	Generation      int64
	ValidBefore     int64
	AllActiveOrders bool
}

func UpdatePaymentOptionsAndRevokeCredentialsWithVersionLockHeld(
	values map[string]string,
	expectedVersion int64,
	revocations []PaymentCredentialRevocation,
) (int64, error) {
	return updatePaymentOptionsWithVersionLockHeld(values, expectedVersion, revocations, nil, nil)
}

func UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
	values map[string]string,
	expectedVersion int64,
	revocations []PaymentCredentialRevocation,
	preconditions *PaymentConfigurationPreconditions,
	audit *PaymentConfigurationAuditInput,
) (int64, error) {
	return updatePaymentOptionsWithVersionLockHeld(values, expectedVersion, revocations, preconditions, audit)
}

func updatePaymentOptionsWithVersionLockHeld(
	values map[string]string,
	expectedVersion int64,
	revocations []PaymentCredentialRevocation,
	preconditions *PaymentConfigurationPreconditions,
	audit *PaymentConfigurationAuditInput,
) (int64, error) {
	if expectedVersion <= 0 {
		return 0, errors.New("expected payment configuration version must be positive")
	}
	if _, exists := values[PaymentConfigurationVersionOptionKey]; exists {
		return 0, errors.New("payment configuration version cannot be updated directly")
	}
	for key, value := range values {
		if err := validateOptionValueBeforePersistence(key, value); err != nil {
			return 0, err
		}
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if audit != nil {
		// Build the durable evidence from the actual mutation rather than
		// trusting a caller-provided summary that could omit a changed key or
		// list a provider that was not revoked.
		audit.ChangedKeys = append([]string(nil), keys...)
		revokedProviderSet := make(map[string]struct{}, len(revocations))
		for _, revocation := range revocations {
			provider := strings.ToLower(strings.TrimSpace(revocation.Provider))
			if provider != "" {
				revokedProviderSet[provider] = struct{}{}
			}
		}
		audit.RevokedProviders = audit.RevokedProviders[:0]
		for provider := range revokedProviderSet {
			audit.RevokedProviders = append(audit.RevokedProviders, provider)
		}
		sort.Strings(audit.RevokedProviders)
		if err := audit.validate(); err != nil {
			return 0, err
		}
	}

	currentVersion := int64(0)
	nextVersion := int64(0)
	affectedOrderIDs := make(map[int64]struct{})
	affectedProjectionIDs := make(map[string]struct{})
	affectedOrders := int64(0)
	affectedEvents := int64(0)
	err := DB.Transaction(func(tx *gorm.DB) error {
		initialVersion := Option{
			Key:   PaymentConfigurationVersionOptionKey,
			Value: strconv.FormatInt(initialPaymentConfigurationVersion, 10),
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoNothing: true,
		}).Create(&initialVersion).Error; err != nil {
			return err
		}

		var storedVersion Option
		if err := lockForUpdate(tx).
			Where(fmt.Sprintf("%s = ?", optionKeyColumn()), PaymentConfigurationVersionOptionKey).
			First(&storedVersion).Error; err != nil {
			return err
		}
		parsedVersion, err := strconv.ParseInt(strings.TrimSpace(storedVersion.Value), 10, 64)
		if err != nil || parsedVersion <= 0 {
			return fmt.Errorf("invalid stored payment configuration version %q", storedVersion.Value)
		}
		currentVersion = parsedVersion
		if currentVersion != expectedVersion {
			return ErrPaymentConfigurationVersionConflict
		}
		if currentVersion == math.MaxInt64 {
			return errors.New("payment configuration version is exhausted")
		}
		if preconditions != nil {
			activeStatuses := []string{PaymentOrderStatusPending, PaymentOrderStatusProcessing, PaymentOrderStatusManualReview}
			callbackDependentProviders := append([]string(nil), preconditions.RequireNoCallbackDependentProviderOrders...)
			if preconditions.RequireNoCallbackDependentOrders {
				callbackDependentProviders = append(callbackDependentProviders,
					PaymentProviderEpay, PaymentProviderStripe, PaymentProviderXorPay)
			}
			seenCallbackProvider := make(map[string]struct{}, len(callbackDependentProviders))
			for _, provider := range callbackDependentProviders {
				provider = strings.TrimSpace(provider)
				if _, seen := seenCallbackProvider[provider]; seen {
					continue
				}
				seenCallbackProvider[provider] = struct{}{}
				count, err := countPaymentOrdersDependingOnCallbackOriginTx(tx, provider, common.GetTimestamp())
				if err != nil {
					return err
				}
				if count > 0 {
					return fmt.Errorf("%w: payment configuration cannot be changed while %s orders still depend on it", ErrPaymentConfigurationPrecondition, provider)
				}
			}
			activeProviders := append([]string(nil), preconditions.RequireNoActiveProviderOrders...)
			if preconditions.RequireNoActiveEpayOrders {
				activeProviders = append(activeProviders, PaymentProviderEpay)
			}
			seenActiveProvider := make(map[string]struct{}, len(activeProviders))
			for _, provider := range activeProviders {
				provider = strings.TrimSpace(provider)
				if _, seen := seenActiveProvider[provider]; seen {
					continue
				}
				seenActiveProvider[provider] = struct{}{}
				count, err := countActivePaymentOrdersForProviderTx(tx, provider)
				if err != nil {
					return err
				}
				if count > 0 {
					return fmt.Errorf("%w: payment configuration cannot be changed while unfinished %s orders depend on it", ErrPaymentConfigurationPrecondition, provider)
				}
			}
			if preconditions.RequireNoActiveStripeOrdersForHostRemoval {
				var count int64
				if err := tx.Model(&PaymentOrder{}).Where("provider = ? AND status IN ?", PaymentProviderStripe, activeStatuses).
					Count(&count).Error; err != nil {
					return err
				}
				legacyCount, err := countLegacyActivePaymentProjectionsTx(tx, PaymentProviderStripe, false)
				if err != nil {
					return err
				}
				if count+legacyCount > 0 {
					return fmt.Errorf("%w: Stripe custom Checkout hosts cannot be removed while unfinished Stripe payment orders may still depend on them", ErrPaymentConfigurationPrecondition)
				}
			}
			if preconditions.RequireNoStripeHistory {
				hasHistory, err := hasStripeAccountBoundData(tx)
				if err != nil {
					return err
				}
				if hasHistory {
					return fmt.Errorf("%w: Stripe configuration cannot be changed while durable Stripe data exists", ErrPaymentConfigurationPrecondition)
				}
			}
			if preconditions.RequireStripeWebhookOverlap {
				var count int64
				if err := tx.Model(&PaymentOrder{}).Where("provider = ? AND status IN ?", PaymentProviderStripe, activeStatuses).
					Count(&count).Error; err != nil {
					return err
				}
				legacyCount, err := countLegacyActivePaymentProjectionsTx(tx, PaymentProviderStripe, false)
				if err != nil {
					return err
				}
				if count+legacyCount > 0 {
					expiresAt, err := strconv.ParseInt(values["StripeWebhookSecretPreviousExpiresAt"], 10, 64)
					if err != nil || values["StripeWebhookSecret"] == "" || values["StripeWebhookSecretPrevious"] == "" || expiresAt <= common.GetTimestamp() {
						return fmt.Errorf("%w: Stripe webhook secret rotation requires an active previous-secret overlap while payment orders are in flight", ErrPaymentConfigurationPrecondition)
					}
				}
			}
		}

		for _, key := range keys {
			storageValue, err := paymentOptionStorageValue(key, values[key])
			if err != nil {
				return err
			}
			option := Option{Key: key, Value: storageValue}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value"}),
			}).Create(&option).Error; err != nil {
				return err
			}
		}
		for _, revocation := range revocations {
			generationRevocation := (revocation.Provider == PaymentProviderEpay || revocation.Provider == PaymentProviderXorPay) &&
				revocation.Generation > 0 && !revocation.AllActiveOrders
			stripeWebhookRevocation := revocation.Provider == PaymentProviderStripe && revocation.Generation > 0
			currentOnlyDisable := paymentProviderUsesCurrentOnlyCredentials(revocation.Provider) &&
				revocation.Generation == 0 && revocation.AllActiveOrders
			if (!generationRevocation && !stripeWebhookRevocation && !currentOnlyDisable) || revocation.ValidBefore <= 0 {
				return errors.New("invalid payment credential revocation")
			}
			now := common.GetTimestamp()
			if err := markCanonicalPaymentCredentialIncidentsTx(tx, revocation, now, affectedOrderIDs); err != nil {
				return err
			}
			if tx.Migrator().HasTable(&PaymentEvent{}) {
				reason := "provider credential generation revoked; event cannot be linked automatically"
				if stripeWebhookRevocation {
					reason = "Stripe webhook signing credential revoked; event cannot be linked automatically"
				} else if currentOnlyDisable {
					reason = "provider current-only credential disabled; event requires manual review"
				}
				eventUpdate := tx.Model(&PaymentEvent{}).
					Where("provider = ? AND provider_credential_generation = ? AND payment_order_id = ?",
						revocation.Provider, revocation.Generation, 0).
					Where("(status IS NULL OR status NOT IN ?)", []string{PaymentEventStatusProcessed, PaymentEventStatusDismissed, PaymentEventStatusCredentialRevoked}).
					Where("paid = ? OR refunded = ? OR disputed = ? OR dispute_resolved = ? OR paid_amount_minor > 0 OR refunded_amount_minor > 0 OR disputed_amount_minor > 0",
						true, true, true, true).
					Updates(map[string]interface{}{
						"status": PaymentEventStatusCredentialRevoked, "last_error": reason,
						"processed_at": now, "updated_at": now,
					})
				if eventUpdate.Error != nil {
					return eventUpdate.Error
				}
				affectedEvents += eventUpdate.RowsAffected
			}
			if (generationRevocation || revocation.AllActiveOrders) && tx.Migrator().HasTable(&TopUp{}) {
				query := tx.Model(&TopUp{}).Where("(payment_order_id IS NULL OR payment_order_id = 0)")
				if stripeWebhookRevocation {
					query = query.Where("status IN ?", []string{
						common.TopUpStatusPending, PaymentOrderStatusProcessing, common.TopUpStatusManualReview,
					})
				} else if currentOnlyDisable {
					recoveryCutoff := revocation.ValidBefore - int64(PaymentCallbackRecoveryWindow/time.Second)
					if recoveryCutoff <= 0 {
						recoveryCutoff = 1
					}
					query = query.Where(
						"(status IN ? OR (status IN ? AND (complete_time >= ? OR create_time >= ?)))",
						[]string{common.TopUpStatusPending, PaymentOrderStatusProcessing, common.TopUpStatusManualReview},
						[]string{common.TopUpStatusFailed, common.TopUpStatusExpired},
						recoveryCutoff, recoveryCutoff,
					)
				} else {
					query = query.Where("status = ? AND create_time <= ?", common.TopUpStatusPending, revocation.ValidBefore)
				}
				if revocation.Provider == PaymentProviderEpay {
					query = query.Where("payment_provider = ? OR payment_provider = ''", revocation.Provider)
				} else {
					query = query.Where("payment_provider = ?", revocation.Provider)
				}
				var projections []TopUp
				if err := query.Select("id").Find(&projections).Error; err != nil {
					return err
				}
				ids := make([]int, 0, len(projections))
				for _, projection := range projections {
					ids = append(ids, projection.Id)
					affectedProjectionIDs["topup:"+strconv.Itoa(projection.Id)] = struct{}{}
				}
				if len(ids) > 0 {
					if err := tx.Model(&TopUp{}).Where("id IN ?", ids).Update("status", common.TopUpStatusManualReview).Error; err != nil {
						return err
					}
				}
			}
			if (generationRevocation || revocation.AllActiveOrders) && tx.Migrator().HasTable(&SubscriptionOrder{}) {
				query := tx.Model(&SubscriptionOrder{}).Where("(payment_order_id IS NULL OR payment_order_id = 0)")
				if stripeWebhookRevocation {
					query = query.Where("status IN ?", []string{
						common.TopUpStatusPending, PaymentOrderStatusProcessing, SubscriptionOrderStatusManualReview,
					})
				} else if currentOnlyDisable {
					recoveryCutoff := revocation.ValidBefore - int64(PaymentCallbackRecoveryWindow/time.Second)
					if recoveryCutoff <= 0 {
						recoveryCutoff = 1
					}
					query = query.Where(
						"(status IN ? OR (status IN ? AND (complete_time >= ? OR create_time >= ?)))",
						[]string{common.TopUpStatusPending, PaymentOrderStatusProcessing, SubscriptionOrderStatusManualReview},
						[]string{common.TopUpStatusFailed, common.TopUpStatusExpired},
						recoveryCutoff, recoveryCutoff,
					)
				} else {
					query = query.Where("status = ? AND create_time <= ?", common.TopUpStatusPending, revocation.ValidBefore)
				}
				if revocation.Provider == PaymentProviderEpay {
					query = query.Where("payment_provider = ? OR payment_provider = ''", revocation.Provider)
				} else {
					query = query.Where("payment_provider = ?", revocation.Provider)
				}
				reason := "provider credential generation revoked; verify payment manually"
				if stripeWebhookRevocation {
					reason = "Stripe webhook signing credential revoked; verify payment manually"
				} else if currentOnlyDisable {
					reason = "provider current-only credential disabled; verify payment manually"
				}
				var projections []SubscriptionOrder
				if err := query.Select("id").Find(&projections).Error; err != nil {
					return err
				}
				ids := make([]int, 0, len(projections))
				for _, projection := range projections {
					ids = append(ids, projection.Id)
					affectedProjectionIDs["subscription:"+strconv.Itoa(projection.Id)] = struct{}{}
				}
				if len(ids) > 0 {
					if err := tx.Model(&SubscriptionOrder{}).Where("id IN ?", ids).Updates(map[string]interface{}{
						"status": SubscriptionOrderStatusManualReview, "review_reason": reason,
					}).Error; err != nil {
						return err
					}
				}
			}
		}

		nextVersion = currentVersion + 1
		affectedOrders = int64(len(affectedOrderIDs) + len(affectedProjectionIDs))
		if audit != nil {
			record, err := newPaymentConfigurationAudit(*audit, currentVersion, nextVersion, affectedOrders, affectedEvents)
			if err != nil {
				return err
			}
			if err := tx.Create(record).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&Option{}).
			Where(fmt.Sprintf("%s = ? AND value = ?", optionKeyColumn()), PaymentConfigurationVersionOptionKey, storedVersion.Value).
			Update("value", strconv.FormatInt(nextVersion, 10))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPaymentConfigurationVersionConflict
		}
		return nil
	})
	if err != nil {
		return currentVersion, err
	}

	for _, key := range keys {
		if err := updateOptionMap(key, values[key]); err != nil {
			reloadErr := loadOptionsFromDatabaseWithPaymentConfigurationLockHeld()
			if reloadErr != nil {
				return nextVersion, fmt.Errorf("apply payment option %s: %w; reload committed configuration: %v", key, err, reloadErr)
			}
			return nextVersion, fmt.Errorf("apply payment option %s: %w", key, err)
		}
	}
	if err := updateOptionMap(PaymentConfigurationVersionOptionKey, strconv.FormatInt(nextVersion, 10)); err != nil {
		reloadErr := loadOptionsFromDatabaseWithPaymentConfigurationLockHeld()
		if reloadErr != nil {
			return nextVersion, fmt.Errorf("apply payment configuration version: %w; reload committed configuration: %v", err, reloadErr)
		}
		return nextVersion, fmt.Errorf("apply payment configuration version: %w", err)
	}
	secretStorageChanged := false
	for _, key := range keys {
		if IsPaymentSecretOption(key) {
			secretStorageChanged = true
			break
		}
	}
	if secretStorageChanged {
		if err := refreshPaymentSecretStorageReadiness(); err != nil {
			return nextVersion, fmt.Errorf("refresh payment secret storage readiness: %w", err)
		}
	}
	return nextVersion, nil
}

func updateOptionMap(key string, value string) (err error) {
	if key == "StripeCheckoutAllowedHosts" {
		value, err = setting.NormalizeStripeCheckoutAllowedHosts(value)
		if err != nil {
			return err
		}
	}
	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	common.OptionMap[key] = value

	// 检查是否是模型配置 - 使用更规范的方式处理
	if handleConfigUpdate(key, value) {
		return nil // 已由配置系统处理
	}

	// 处理传统配置项...
	if strings.HasSuffix(key, "Permission") {
		intValue, _ := strconv.Atoi(value)
		switch key {
		case "FileUploadPermission":
			common.FileUploadPermission = intValue
		case "FileDownloadPermission":
			common.FileDownloadPermission = intValue
		case "ImageUploadPermission":
			common.ImageUploadPermission = intValue
		case "ImageDownloadPermission":
			common.ImageDownloadPermission = intValue
		}
	}
	if strings.HasSuffix(key, "Enabled") || key == "DefaultCollapseSidebar" || key == "DefaultUseAutoGroup" || key == "SMTPForceAuthLogin" || key == "SMTPInsecureSkipVerify" {
		boolValue := value == "true"
		switch key {
		case "PasswordRegisterEnabled":
			common.PasswordRegisterEnabled = boolValue
		case "PasswordLoginEnabled":
			common.PasswordLoginEnabled = boolValue
		case "EmailVerificationEnabled":
			common.EmailVerificationEnabled = boolValue
		case "GitHubOAuthEnabled":
			common.GitHubOAuthEnabled = boolValue
		case "LinuxDOOAuthEnabled":
			common.LinuxDOOAuthEnabled = boolValue
		case "WeChatAuthEnabled":
			common.WeChatAuthEnabled = boolValue
		case "TelegramOAuthEnabled":
			common.TelegramOAuthEnabled = boolValue
		case "TurnstileCheckEnabled":
			common.TurnstileCheckEnabled = boolValue
		case "RegisterEnabled":
			common.RegisterEnabled = boolValue
		case "EmailDomainRestrictionEnabled":
			common.EmailDomainRestrictionEnabled = boolValue
		case "EmailAliasRestrictionEnabled":
			common.EmailAliasRestrictionEnabled = boolValue
		case "AutomaticDisableChannelEnabled":
			common.AutomaticDisableChannelEnabled = boolValue
		case "AutomaticEnableChannelEnabled":
			common.AutomaticEnableChannelEnabled = boolValue
		case "LogConsumeEnabled":
			common.LogConsumeEnabled = boolValue
		case "DisplayInCurrencyEnabled":
			// 兼容旧字段：同步到新配置 general_setting.quota_display_type（运行时生效）
			// true -> USD, false -> TOKENS
			newVal := "USD"
			if !boolValue {
				newVal = "TOKENS"
			}
			if cfg := config.GlobalConfig.Get("general_setting"); cfg != nil {
				_ = config.UpdateConfigFromMap(cfg, map[string]string{"quota_display_type": newVal})
			}
		case "DisplayTokenStatEnabled":
			common.DisplayTokenStatEnabled = boolValue
		case "DrawingEnabled":
			common.DrawingEnabled = boolValue
		case "TaskEnabled":
			common.TaskEnabled = boolValue
		case "DataExportEnabled":
			common.DataExportEnabled = boolValue
		case "DefaultCollapseSidebar":
			common.DefaultCollapseSidebar = boolValue
		case "MjNotifyEnabled":
			setting.MjNotifyEnabled = boolValue
		case "MjAccountFilterEnabled":
			setting.MjAccountFilterEnabled = boolValue
		case "MjModeClearEnabled":
			setting.MjModeClearEnabled = boolValue
		case "MjForwardUrlEnabled":
			setting.MjForwardUrlEnabled = boolValue
		case "MjActionCheckSuccessEnabled":
			setting.MjActionCheckSuccessEnabled = boolValue
		case "CheckSensitiveEnabled":
			setting.CheckSensitiveEnabled = boolValue
		case "DemoSiteEnabled":
			operation_setting.DemoSiteEnabled = boolValue
		case "SelfUseModeEnabled":
			operation_setting.SelfUseModeEnabled = boolValue
		case "CheckSensitiveOnPromptEnabled":
			setting.CheckSensitiveOnPromptEnabled = boolValue
		case "ModelRequestRateLimitEnabled":
			setting.ModelRequestRateLimitEnabled = boolValue
		case "StopOnSensitiveEnabled":
			setting.StopOnSensitiveEnabled = boolValue
		case "SMTPSSLEnabled":
			common.SMTPSSLEnabled = boolValue
		case "SMTPStartTLSEnabled":
			common.SMTPStartTLSEnabled = boolValue
		case "SMTPInsecureSkipVerify":
			common.SMTPInsecureSkipVerify = boolValue
		case "SMTPForceAuthLogin":
			common.SMTPForceAuthLogin = boolValue
		case "WorkerAllowHttpImageRequestEnabled":
			system_setting.WorkerAllowHttpImageRequestEnabled = boolValue
		case "DefaultUseAutoGroup":
			setting.DefaultUseAutoGroup = boolValue
		case "ExposeRatioEnabled":
			ratio_setting.SetExposeRatioEnabled(boolValue)
		}
	}
	switch key {
	case "EmailDomainWhitelist":
		common.EmailDomainWhitelist = strings.Split(value, ",")
	case "SMTPServer":
		common.SMTPServer = value
	case "SMTPPort":
		intValue, _ := strconv.Atoi(value)
		common.SMTPPort = intValue
	case "SMTPAccount":
		common.SMTPAccount = value
	case "SMTPFrom":
		common.SMTPFrom = value
	case "SMTPToken":
		common.SMTPToken = value
	case "ServerAddress":
		system_setting.ServerAddress = value
	case "WorkerUrl":
		system_setting.WorkerUrl = value
	case "WorkerValidKey":
		system_setting.WorkerValidKey = value
	case "PayAddress":
		operation_setting.PayAddress = value
	case "Chats":
		err = setting.UpdateChatsByJsonString(value)
	case "AutoGroups":
		err = setting.UpdateAutoGroupsByJsonString(value)
	case "CustomCallbackAddress":
		operation_setting.CustomCallbackAddress = value
	case "EpayId":
		operation_setting.EpayId = value
	case "EpayKey":
		operation_setting.EpayKey = value
	case "EpayCurrency":
		operation_setting.EpayCurrency = strings.ToUpper(strings.TrimSpace(value))
	case "EpayCredentialGeneration":
		generation, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || generation <= 0 {
			return errors.New("invalid Epay credential generation")
		}
		operation_setting.EpayCredentialGeneration = generation
	case "EpayIdPrevious":
		operation_setting.EpayIdPrevious = strings.TrimSpace(value)
	case "EpayKeyPrevious":
		operation_setting.EpayKeyPrevious = value
	case "EpayPreviousCredentialGeneration":
		generation, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || generation < 0 {
			return errors.New("invalid previous Epay credential generation")
		}
		operation_setting.EpayPreviousCredentialGeneration = generation
	case "EpayPreviousValidBefore":
		validBefore, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || validBefore < 0 {
			return errors.New("invalid previous Epay credential boundary")
		}
		operation_setting.EpayPreviousValidBefore = validBefore
	case "EpayPreviousExpiresAt":
		expiresAt, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || expiresAt < 0 {
			return errors.New("invalid previous Epay credential expiry")
		}
		operation_setting.EpayPreviousExpiresAt = expiresAt
	case "Price":
		operation_setting.Price, _ = strconv.ParseFloat(value, 64)
	case "USDExchangeRate":
		operation_setting.USDExchangeRate, _ = strconv.ParseFloat(value, 64)
	case "MinTopUp":
		operation_setting.MinTopUp, _ = strconv.Atoi(value)
	case "StripeApiSecret":
		setting.StripeApiSecret = value
	case "StripeWebhookSecret":
		setting.StripeWebhookSecret = value
	case "StripeWebhookSecretPrevious":
		setting.StripeWebhookSecretPrevious = value
	case "StripeWebhookSecretPreviousExpiresAt":
		setting.StripeWebhookSecretPreviousExpiresAt, _ = strconv.ParseInt(value, 10, 64)
	case "StripeWebhookCredentialGeneration":
		generation, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || generation <= 0 {
			return errors.New("invalid Stripe webhook credential generation")
		}
		setting.StripeWebhookCredentialGeneration = generation
	case "StripeWebhookPreviousCredentialGeneration":
		generation, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || generation < 0 {
			return errors.New("invalid previous Stripe webhook credential generation")
		}
		setting.StripeWebhookPreviousCredentialGeneration = generation
	case "StripeWebhookPreviousValidBefore":
		validBefore, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || validBefore < 0 {
			return errors.New("invalid previous Stripe webhook credential boundary")
		}
		setting.StripeWebhookPreviousValidBefore = validBefore
	case "StripePriceId":
		setting.StripePriceId = value
	case "StripeCurrency":
		setting.StripeCurrency = strings.ToUpper(strings.TrimSpace(value))
	case "StripeAccountId":
		setting.StripeAccountId = strings.TrimSpace(value)
	case "StripeCheckoutAllowedHosts":
		setting.StripeCheckoutAllowedHosts = value
	case "StripeCredentialAccountId":
		setting.StripeCredentialAccountId = strings.TrimSpace(value)
	case "StripeCredentialLivemode":
		mode := strings.ToLower(strings.TrimSpace(value))
		if mode != "" && mode != "test" && mode != "live" {
			return errors.New("invalid Stripe credential livemode")
		}
		setting.StripeCredentialLivemode = mode
	case "StripeWebhookCredentialLivemode":
		mode := strings.ToLower(strings.TrimSpace(value))
		if mode != "" && mode != "test" && mode != "live" {
			return errors.New("invalid Stripe webhook credential livemode")
		}
		setting.StripeWebhookCredentialLivemode = mode
	case "StripeConfigurationVerifiedFingerprint":
		fingerprint := strings.ToLower(strings.TrimSpace(value))
		if fingerprint != "" && (len(fingerprint) != 64 || strings.Trim(fingerprint, "0123456789abcdef") != "") {
			return errors.New("invalid Stripe configuration verification fingerprint")
		}
		setting.StripeConfigurationVerifiedFingerprint = fingerprint
	case "StripeConfigurationVerifiedAt":
		verifiedAt, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || verifiedAt < 0 {
			return errors.New("invalid Stripe configuration verification time")
		}
		setting.StripeConfigurationVerifiedAt = verifiedAt
	case "StripeUnitPrice":
		setting.StripeUnitPrice, _ = strconv.ParseFloat(value, 64)
	case "StripeMinTopUp":
		setting.StripeMinTopUp, _ = strconv.Atoi(value)
	case "StripePromotionCodesEnabled":
		setting.StripePromotionCodesEnabled = value == "true"
	case "XorPayAid":
		setting.XorPayAid = strings.TrimSpace(value)
	case "XorPayAppSecret":
		setting.XorPayAppSecret = value
	case "XorPayCredentialGeneration":
		generation, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || generation <= 0 {
			return errors.New("invalid XORPay credential generation")
		}
		setting.XorPayCredentialGeneration = generation
	case "XorPayAidPrevious":
		setting.XorPayAidPrevious = strings.TrimSpace(value)
	case "XorPayAppSecretPrevious":
		setting.XorPayAppSecretPrevious = value
	case "XorPayPreviousCredentialGeneration":
		generation, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || generation < 0 {
			return errors.New("invalid previous XORPay credential generation")
		}
		setting.XorPayPreviousCredentialGeneration = generation
	case "XorPayPreviousValidBefore":
		validBefore, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || validBefore < 0 {
			return errors.New("invalid previous XORPay credential boundary")
		}
		setting.XorPayPreviousValidBefore = validBefore
	case "XorPayPreviousExpiresAt":
		expiresAt, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || expiresAt < 0 {
			return errors.New("invalid previous XORPay credential expiry")
		}
		setting.XorPayPreviousExpiresAt = expiresAt
	case "XorPayUnitPrice":
		setting.XorPayUnitPrice, _ = strconv.ParseFloat(value, 64)
	case "XorPayMinTopUp":
		setting.XorPayMinTopUp, _ = strconv.Atoi(value)
	case "XorPayCurrency":
		setting.XorPayCurrency = strings.ToUpper(strings.TrimSpace(value))
	case "XorPayEnabledMethods":
		err = setting.UpdateXorPayEnabledMethodsByJsonString(value)
	case "CreemApiKey":
		setting.CreemApiKey = value
	case "CreemProducts":
		setting.CreemProducts = value
	case "CreemTestMode":
		setting.CreemTestMode = value == "true"
	case "CreemWebhookSecret":
		setting.CreemWebhookSecret = value
	case "WaffoEnabled":
		setting.WaffoEnabled = value == "true"
	case "WaffoApiKey":
		setting.WaffoApiKey = value
	case "WaffoPrivateKey":
		setting.WaffoPrivateKey = value
	case "WaffoPublicCert":
		setting.WaffoPublicCert = value
	case "WaffoSandboxPublicCert":
		setting.WaffoSandboxPublicCert = value
	case "WaffoSandboxApiKey":
		setting.WaffoSandboxApiKey = value
	case "WaffoSandboxPrivateKey":
		setting.WaffoSandboxPrivateKey = value
	case "WaffoSandbox":
		setting.WaffoSandbox = value == "true"
	case "WaffoMerchantId":
		setting.WaffoMerchantId = value
	case "WaffoNotifyUrl":
		setting.WaffoNotifyUrl = value
	case "WaffoReturnUrl":
		setting.WaffoReturnUrl = value
	case "WaffoSubscriptionReturnUrl":
		setting.WaffoSubscriptionReturnUrl = value
	case "WaffoWebRedirectHosts":
		setting.WaffoWebRedirectHosts = value
	case "WaffoAppRedirectSchemes":
		setting.WaffoAppRedirectSchemes = value
	case "WaffoCurrency":
		setting.WaffoCurrency = value
	case "WaffoUnitPrice":
		setting.WaffoUnitPrice, _ = strconv.ParseFloat(value, 64)
	case "WaffoMinTopUp":
		setting.WaffoMinTopUp, _ = strconv.Atoi(value)
	case "WaffoPancakeMerchantID":
		setting.WaffoPancakeMerchantID = value
	case "WaffoPancakePrivateKey":
		setting.WaffoPancakePrivateKey = value
	case "WaffoPancakeReturnURL":
		setting.WaffoPancakeReturnURL = value
	case "WaffoPancakeTestMode":
		setting.WaffoPancakeTestMode = value == "true"
	case "WaffoPancakeStoreID":
		setting.WaffoPancakeStoreID = value
	case "WaffoPancakeProductID":
		setting.WaffoPancakeProductID = value
	case "WaffoPancakeUnitPrice":
		setting.WaffoPancakeUnitPrice, _ = strconv.ParseFloat(value, 64)
	case "WaffoPancakeMinTopUp":
		setting.WaffoPancakeMinTopUp, _ = strconv.Atoi(value)
	case "TopupGroupRatio":
		err = common.UpdateTopupGroupRatioByJSONString(value)
	case "GitHubClientId":
		common.GitHubClientId = value
	case "GitHubClientSecret":
		common.GitHubClientSecret = value
	case "LinuxDOClientId":
		common.LinuxDOClientId = value
	case "LinuxDOClientSecret":
		common.LinuxDOClientSecret = value
	case "LinuxDOMinimumTrustLevel":
		common.LinuxDOMinimumTrustLevel, _ = strconv.Atoi(value)
	case "Footer":
		common.Footer = value
	case "SystemName":
		common.SystemName = value
	case "Logo":
		common.Logo = value
	case "WeChatServerAddress":
		common.WeChatServerAddress = value
	case "WeChatServerToken":
		common.WeChatServerToken = value
	case "WeChatAccountQRCodeImageURL":
		common.WeChatAccountQRCodeImageURL = value
	case "TelegramBotToken":
		common.TelegramBotToken = value
	case "TelegramBotName":
		common.TelegramBotName = value
	case "TurnstileSiteKey":
		common.TurnstileSiteKey = value
	case "TurnstileSecretKey":
		common.TurnstileSecretKey = value
	case "QuotaForNewUser":
		common.QuotaForNewUser, _ = strconv.Atoi(value)
	case "QuotaForInviter":
		common.QuotaForInviter, _ = strconv.Atoi(value)
	case "QuotaForInvitee":
		common.QuotaForInvitee, _ = strconv.Atoi(value)
	case "QuotaRemindThreshold":
		common.QuotaRemindThreshold, _ = strconv.Atoi(value)
	case "PreConsumedQuota":
		common.PreConsumedQuota, _ = strconv.Atoi(value)
	case "ModelRequestRateLimitCount":
		setting.ModelRequestRateLimitCount, _ = strconv.Atoi(value)
	case "ModelRequestRateLimitDurationMinutes":
		setting.ModelRequestRateLimitDurationMinutes, _ = strconv.Atoi(value)
	case "ModelRequestRateLimitSuccessCount":
		setting.ModelRequestRateLimitSuccessCount, _ = strconv.Atoi(value)
	case "ModelRequestRateLimitGroup":
		err = setting.UpdateModelRequestRateLimitGroupByJSONString(value)
	case "RetryTimes":
		common.RetryTimes, _ = strconv.Atoi(value)
	case "DataExportInterval":
		common.DataExportInterval, _ = strconv.Atoi(value)
	case "DataExportDefaultTime":
		common.DataExportDefaultTime = value
	case "ModelRatio":
		err = ratio_setting.UpdateModelRatioByJSONString(value)
	case "GroupRatio":
		err = ratio_setting.UpdateGroupRatioByJSONString(value)
	case "GroupGroupRatio":
		err = ratio_setting.UpdateGroupGroupRatioByJSONString(value)
	case "UserUsableGroups":
		err = setting.UpdateUserUsableGroupsByJSONString(value)
	case "CompletionRatio":
		err = ratio_setting.UpdateCompletionRatioByJSONString(value)
	case "ModelPrice":
		err = ratio_setting.UpdateModelPriceByJSONString(value)
	case "CacheRatio":
		err = ratio_setting.UpdateCacheRatioByJSONString(value)
	case "CreateCacheRatio":
		err = ratio_setting.UpdateCreateCacheRatioByJSONString(value)
	case "ImageRatio":
		err = ratio_setting.UpdateImageRatioByJSONString(value)
	case "AudioRatio":
		err = ratio_setting.UpdateAudioRatioByJSONString(value)
	case "AudioCompletionRatio":
		err = ratio_setting.UpdateAudioCompletionRatioByJSONString(value)
	case "TopUpLink":
		common.TopUpLink = value
	//case "ChatLink":
	//	common.ChatLink = value
	//case "ChatLink2":
	//	common.ChatLink2 = value
	case "ChannelDisableThreshold":
		common.ChannelDisableThreshold, _ = strconv.ParseFloat(value, 64)
	case "QuotaPerUnit":
		common.QuotaPerUnit, _ = strconv.ParseFloat(value, 64)
	case "SensitiveWords":
		setting.SensitiveWordsFromString(value)
	case "AutomaticDisableKeywords":
		operation_setting.AutomaticDisableKeywordsFromString(value)
	case "AutomaticDisableStatusCodes":
		err = operation_setting.AutomaticDisableStatusCodesFromString(value)
	case "AutomaticRetryStatusCodes":
		err = operation_setting.AutomaticRetryStatusCodesFromString(value)
	case "StreamCacheQueueLength":
		setting.StreamCacheQueueLength, _ = strconv.Atoi(value)
	case "PayMethods":
		err = operation_setting.UpdatePayMethodsByJsonString(value)
	case "WaffoPayMethods":
		// WaffoPayMethods is read directly from OptionMap via setting.GetWaffoPayMethods().
		// The value is already stored in OptionMap at the top of this function (line: common.OptionMap[key] = value).
		// No additional in-memory variable to update.
	}
	return err
}

// handleConfigUpdate 处理分层配置更新，返回是否已处理
func handleConfigUpdate(key, value string) bool {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 {
		return false // 不是分层配置
	}

	configName := parts[0]
	configKey := parts[1]

	// 获取配置对象
	cfg := config.GlobalConfig.Get(configName)
	if cfg == nil {
		return false // 未注册的配置
	}

	// 更新配置
	configMap := map[string]string{
		configKey: value,
	}
	config.UpdateConfigFromMap(cfg, configMap)

	// 特定配置的后处理
	if configName == "performance_setting" {
		performance_setting.UpdateAndSync()
	} else if configName == "tool_price_setting" {
		operation_setting.RebuildToolPriceIndex()
	} else if configName == "billing_setting" {
		InvalidatePricingCache()
		ratio_setting.InvalidateExposedDataCache()
	} else if configName == "theme" {
		system_setting.UpdateAndSyncTheme()
	}

	return true // 已处理
}
