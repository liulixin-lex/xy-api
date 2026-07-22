/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import React, { useCallback, useEffect, useState } from 'react';
import {
  Banner,
  Button,
  Card,
  Modal,
  Spin,
  Tabs,
  TextArea,
} from '@douyinfe/semi-ui';
import SettingsGeneralPayment from '../../pages/Setting/Payment/SettingsGeneralPayment';
import SettingsPaymentGateway from '../../pages/Setting/Payment/SettingsPaymentGateway';
import SettingsPaymentGatewayStripe from '../../pages/Setting/Payment/SettingsPaymentGatewayStripe';
import SettingsPaymentGatewayXorPay from '../../pages/Setting/Payment/SettingsPaymentGatewayXorPay';
import SettingsPaymentGatewayCreem from '../../pages/Setting/Payment/SettingsPaymentGatewayCreem';
import SettingsPaymentGatewayWaffo from '../../pages/Setting/Payment/SettingsPaymentGatewayWaffo';
import SettingsPaymentGatewayWaffoPancake from '../../pages/Setting/Payment/SettingsPaymentGatewayWaffoPancake';
import PaymentOverviewPanel from '../../pages/Setting/Payment/PaymentOverviewPanel';
import PaymentLimitsPanel from '../../pages/Setting/Payment/PaymentLimitsPanel';
import StripeLegacyInventoryPanel from '../../pages/Setting/Payment/StripeLegacyInventoryPanel';
import {
  API,
  showError,
  showSuccess,
  showWarning,
  toBoolean,
} from '../../helpers';
import { useTranslation } from 'react-i18next';
import RiskAcknowledgementModal from '../common/modals/RiskAcknowledgementModal';
import SecureVerificationModal from '../common/modals/SecureVerificationModal';
import { useSecureVerification } from '../../hooks/common/useSecureVerification';
import {
  getEmergencyCredentialClearSecrets,
  isEmergencyCredentialRevocationReasonValid,
  normalizeEmergencyCredentialRevocationReason,
  resolveEmergencyCredentialRevocationMode,
} from '../../helpers/payment-credential-revocation';

const CURRENT_COMPLIANCE_TERMS_VERSION = 'v1';
const PAYMENT_CONFIG_VERSION_KEY = 'payment_setting.config_version';
const DEFAULT_PAYMENT_CONFIG_VERSION = 1;

const normalizePaymentConfigVersion = (value) => {
  const version = Number(value);
  return Number.isSafeInteger(version) && version > 0
    ? version
    : DEFAULT_PAYMENT_CONFIG_VERSION;
};

const PaymentSetting = () => {
  const { t } = useTranslation();
  let [inputs, setInputs] = useState({
    ServerAddress: '',
    PayAddress: '',
    EpayId: '',
    EpayKey: '',
    Price: 7.3,
    MinTopUp: 1,
    TopupGroupRatio: '',
    CustomCallbackAddress: '',
    PayMethods: '',
    AmountOptions: '',
    AmountDiscount: '',

    StripeApiSecret: '',
    StripeWebhookSecret: '',
    StripePriceId: '',
    StripeAccountId: '',
    StripeCheckoutAllowedHosts: '',
    StripeCredentialAccountId: '',
    StripeCredentialLivemode: '',
    StripeUnitPrice: 8.0,
    StripeMinTopUp: 1,
    StripeCurrency: 'USD',
    StripePromotionCodesEnabled: false,
    XorPayAid: '',
    XorPayAppSecret: '',
    XorPayUnitPrice: 7.3,
    XorPayMinTopUp: 1,
    XorPayEnabledMethods: [],
    WaffoPancakeUnitPrice: 1,
    WaffoPancakeMinTopUp: 1,
    WaffoPancakeTestMode: false,

    'payment_setting.compliance_confirmed': false,
    'payment_setting.compliance_terms_version': '',
    'payment_setting.compliance_confirmed_at': 0,
    'payment_setting.compliance_confirmed_by': 0,
    'payment_setting.epay_previous_credential_active': false,
    'payment_setting.stripe_previous_credential_active': false,
    'payment_setting.xorpay_previous_credential_active': false,
    'payment_setting.stripe_test_mode_enabled': false,
    'payment_setting.stripe_test_mode_blocked': false,
    'payment_setting.stripe_test_mode_isolation_required': true,
    [PAYMENT_CONFIG_VERSION_KEY]: DEFAULT_PAYMENT_CONFIG_VERSION,
  });

  let [loading, setLoading] = useState(false);
  const [complianceVisible, setComplianceVisible] = useState(false);
  const [pendingCredentialRevocation, setPendingCredentialRevocation] =
    useState(null);
  const [credentialRevocationReason, setCredentialRevocationReason] =
    useState('');
  const {
    isModalVisible: paymentVerificationVisible,
    verificationMethods: paymentVerificationMethods,
    verificationState: paymentVerificationState,
    executeVerification: executePaymentVerification,
    cancelVerification: cancelPaymentVerification,
    setVerificationCode: setPaymentVerificationCode,
    switchVerificationMethod: switchPaymentVerificationMethod,
    withVerification,
  } = useSecureVerification();
  const complianceStatements = [
    t('你已合法取得所接入模型 API、账号、密钥和额度的授权；'),
    t(
      '你承诺仅在已取得上游服务商、模型服务提供方或相关权利方合法授权的范围内使用其 API、账号、密钥、额度及服务能力，不进行未经授权的转售、倒卖、分销或其他违规商业化使用。',
    ),
    t(
      '如向中华人民共和国境内公众提供生成式人工智能服务，你将依法履行备案登记、安全评估、内容安全、投诉举报、生成合成内容标识、日志留存、个人信息保护等合规义务；',
    ),
    t(
      '你承诺不会利用本系统实施、协助实施或变相实施违反适用法律法规、监管要求、平台规则、社会公共利益或第三方合法权益的行为。',
    ),
    t('你理解并自行承担部署、运营和收费行为产生的法律责任。'),
    t(
      '你理解本合规提醒仅用于风险提示，不构成法律意见、合规审查结论或对你使用本系统行为合法性的保证；你应根据实际业务场景自行咨询专业法律或合规顾问。',
    ),
  ];
  const requiredComplianceText = t(
    '我已阅读并理解上述合规提醒，知悉相关法律风险，并确认自行承担部署、运营和收费行为产生的法律责任',
  );
  const requiredComplianceTextParts = [
    {
      type: 'input',
      text: t('我已阅读并理解上述合规提醒'),
    },
    { type: 'static', text: t('，') },
    {
      type: 'input',
      text: t('知悉相关法律风险'),
    },
    { type: 'static', text: t('，') },
    {
      type: 'input',
      text: t('并确认自行承担部署'),
    },
    { type: 'static', text: t('、') },
    {
      type: 'input',
      text: t('运营和收费行为产生的法律责任'),
    },
  ];
  const complianceConfirmed =
    inputs['payment_setting.compliance_confirmed'] &&
    inputs['payment_setting.compliance_terms_version'] ===
      CURRENT_COMPLIANCE_TERMS_VERSION;

  const getOptions = useCallback(async () => {
    const res = await API.get('/api/option/', { skipErrorHandler: true });
    const { success, message, data } = res.data;
    if (success) {
      let newInputs = {};
      data.forEach((item) => {
        switch (item.key) {
          case 'TopupGroupRatio':
            try {
              newInputs[item.key] = JSON.stringify(
                JSON.parse(item.value),
                null,
                2,
              );
            } catch (error) {
              newInputs[item.key] = item.value;
            }
            break;
          case 'payment_setting.amount_options':
            try {
              newInputs['AmountOptions'] = JSON.stringify(
                JSON.parse(item.value),
                null,
                2,
              );
            } catch (error) {
              newInputs['AmountOptions'] = item.value;
            }
            break;
          case 'payment_setting.amount_discount':
            try {
              newInputs['AmountDiscount'] = JSON.stringify(
                JSON.parse(item.value),
                null,
                2,
              );
            } catch (error) {
              newInputs['AmountDiscount'] = item.value;
            }
            break;
          case 'payment_setting.compliance_confirmed':
          case 'payment_setting.epay_previous_credential_active':
          case 'payment_setting.stripe_previous_credential_active':
          case 'payment_setting.xorpay_previous_credential_active':
          case 'payment_setting.stripe_test_mode_enabled':
          case 'payment_setting.stripe_test_mode_blocked':
          case 'payment_setting.stripe_test_mode_isolation_required':
          case 'WaffoPancakeTestMode':
            newInputs[item.key] = toBoolean(item.value);
            break;
          case 'payment_setting.compliance_confirmed_at':
          case 'payment_setting.compliance_confirmed_by':
            newInputs[item.key] = parseInt(item.value) || 0;
            break;
          case 'payment_setting.compliance_terms_version':
            newInputs[item.key] = item.value;
            break;
          case PAYMENT_CONFIG_VERSION_KEY:
            newInputs[item.key] = normalizePaymentConfigVersion(item.value);
            break;
          case 'Price':
          case 'MinTopUp':
          case 'StripeUnitPrice':
          case 'StripeMinTopUp':
          case 'XorPayUnitPrice':
          case 'XorPayMinTopUp':
          case 'WaffoPancakeUnitPrice':
          case 'WaffoPancakeMinTopUp':
            newInputs[item.key] = parseFloat(item.value);
            break;
          case 'XorPayEnabledMethods':
            try {
              newInputs[item.key] = JSON.parse(item.value || '[]');
            } catch {
              newInputs[item.key] = [];
            }
            break;
          default:
            if (item.key.endsWith('Enabled')) {
              newInputs[item.key] = toBoolean(item.value);
            } else {
              newInputs[item.key] = item.value;
            }
            break;
        }
      });

      setInputs((prev) => ({
        ...prev,
        ...newInputs,
        [PAYMENT_CONFIG_VERSION_KEY]: Math.max(
          normalizePaymentConfigVersion(prev[PAYMENT_CONFIG_VERSION_KEY]),
          normalizePaymentConfigVersion(newInputs[PAYMENT_CONFIG_VERSION_KEY]),
        ),
      }));
    } else {
      throw new Error(t(message || '刷新失败'));
    }
  }, [t]);

  const refreshPaymentOptions = useCallback(
    async (nextConfigVersion, notifyError) => {
      if (nextConfigVersion !== undefined) {
        setInputs((prev) => ({
          ...prev,
          [PAYMENT_CONFIG_VERSION_KEY]: Math.max(
            normalizePaymentConfigVersion(prev[PAYMENT_CONFIG_VERSION_KEY]),
            normalizePaymentConfigVersion(nextConfigVersion),
          ),
        }));
      }
      try {
        setLoading(true);
        await getOptions();
        return true;
      } catch (error) {
        if (notifyError) {
          showError(error?.message || t('刷新失败'));
        }
        return false;
      } finally {
        setLoading(false);
      }
    },
    [getOptions, t],
  );

  const onRefresh = useCallback(
    (nextConfigVersion) => refreshPaymentOptions(nextConfigVersion, true),
    [refreshPaymentOptions],
  );

  const refreshAfterSave = useCallback(
    (nextConfigVersion) => refreshPaymentOptions(nextConfigVersion, false),
    [refreshPaymentOptions],
  );

  const withPaymentVerification = useCallback(
    (apiCall, verificationOptions = {}) => {
      const guardedCall = async () => {
        try {
          return await apiCall();
        } catch (error) {
          if (error?.response?.status === 409) {
            await refreshAfterSave();
          }
          throw error;
        }
      };
      return withVerification(guardedCall, {
        preferredMethod: 'passkey',
        title: verificationOptions.title || t('验证支付设置变更'),
        description:
          verificationOptions.description ||
          t('修改支付凭据或网关配置前，请先确认身份。'),
      });
    },
    [refreshAfterSave, t, withVerification],
  );

  useEffect(() => {
    onRefresh();
  }, [onRefresh]);

  const confirmCompliance = async () => {
    setLoading(true);
    try {
      await withPaymentVerification(async () => {
        const res = await API.post(
          '/api/option/payment_compliance',
          { confirmed: true },
          { skipErrorHandler: true },
        );
        if (!res.data.success) {
          throw new Error(res.data.message || t('确认失败'));
        }
        setComplianceVisible(false);
        const refreshed = await refreshAfterSave();
        if (refreshed) {
          showSuccess(t('合规声明确认成功'));
        } else {
          showWarning(t('已确认合规声明，但最新支付设置状态刷新失败'));
        }
        return res;
      });
    } catch (error) {
      showError(
        error?.response?.data?.message || error?.message || t('确认失败'),
      );
    } finally {
      setLoading(false);
    }
  };

  const requestEmergencyCredentialRevocation = (
    provider,
    replacement = { state: 'none', options: {} },
    requestedMode = null,
  ) => {
    const previousCredentialStateKeys = {
      epay: 'payment_setting.epay_previous_credential_active',
      stripe: 'payment_setting.stripe_previous_credential_active',
      xorpay: 'payment_setting.xorpay_previous_credential_active',
    };
    const mode =
      requestedMode === 'stripe_disable_all' && provider === 'stripe'
        ? requestedMode
        : resolveEmergencyCredentialRevocationMode(
            provider,
            replacement.state,
            Boolean(inputs[previousCredentialStateKeys[provider]]),
          );
    if (!mode && replacement.state === 'partial') {
      showError(
        t(
          'The replacement credential pair is incomplete. Enter both the identifier and secret, or restore the saved identifier before using this emergency action.',
        ),
      );
      return;
    }
    if (!mode) {
      showError(
        t(
          'No active previous {{provider}} credential is available to revoke. Enter a complete replacement identifier and secret to perform an emergency replacement.',
          { provider: provider === 'epay' ? t('易支付') : t('XORPay') },
        ),
      );
      return;
    }
    setCredentialRevocationReason('');
    setPendingCredentialRevocation({
      provider,
      mode,
      options: replacement.options,
    });
  };

  const confirmEmergencyCredentialRevocation = async () => {
    if (
      !pendingCredentialRevocation ||
      !isEmergencyCredentialRevocationReasonValid(credentialRevocationReason)
    ) {
      return;
    }
    setLoading(true);
    try {
      await withPaymentVerification(async () => {
        const clearSecrets = getEmergencyCredentialClearSecrets(
          pendingCredentialRevocation.mode,
        );
        const response = await API.put(
          '/api/option/payment',
          {
            options: pendingCredentialRevocation.options,
            ...(clearSecrets.length > 0 ? { clear_secrets: clearSecrets } : {}),
            revoke_previous_credentials: [pendingCredentialRevocation.provider],
            reason: normalizeEmergencyCredentialRevocationReason(
              credentialRevocationReason,
            ),
            expected_version: normalizePaymentConfigVersion(
              inputs[PAYMENT_CONFIG_VERSION_KEY],
            ),
          },
          { skipErrorHandler: true },
        );
        if (!response.data?.success) {
          throw new Error(response.data?.message || t('撤销失败'));
        }
        const providerLabel =
          credentialRevocationProviderLabels[
            pendingCredentialRevocation.provider
          ] || pendingCredentialRevocation.provider;
        setPendingCredentialRevocation(null);
        setCredentialRevocationReason('');
        const refreshed = await refreshAfterSave(response.data?.data?.version);
        let successMessage = t('旧凭据已撤销');
        if (pendingCredentialRevocation.mode === 'replace') {
          successMessage = t(
            '{{provider}} credentials replaced and compromised generations revoked',
            { provider: providerLabel },
          );
        } else if (pendingCredentialRevocation.mode === 'stripe_disable') {
          successMessage = t(
            'Stripe webhooks disabled and signing credentials revoked',
          );
        } else if (pendingCredentialRevocation.mode === 'stripe_disable_all') {
          successMessage = t(
            'Stripe API and webhook credentials disabled; affected orders quarantined',
          );
        }
        if (refreshed) {
          showSuccess(successMessage);
        } else {
          showWarning(
            t('{{action}}，但最新支付设置状态刷新失败', {
              action: successMessage,
            }),
          );
        }
        return response;
      });
    } catch (error) {
      showError(
        error?.response?.data?.message || error?.message || t('撤销失败'),
      );
    } finally {
      setLoading(false);
    }
  };

  const credentialRevocationProviderLabels = {
    epay: t('易支付'),
    stripe: t('Stripe webhook'),
    xorpay: t('XORPay'),
  };
  const pendingCredentialRevocationProvider =
    pendingCredentialRevocation?.provider;
  const pendingCredentialRevocationProviderLabel =
    credentialRevocationProviderLabels[pendingCredentialRevocationProvider] ||
    pendingCredentialRevocationProvider ||
    '';
  const credentialRevocationReasonValid =
    isEmergencyCredentialRevocationReasonValid(credentialRevocationReason);
  let credentialRevocationWarning = '';
  let credentialRevocationTitle = t('确认立即撤销 {{provider}} 旧凭据？', {
    provider: pendingCredentialRevocationProviderLabel,
  });
  let credentialRevocationConfirmText = t('继续撤销');
  if (pendingCredentialRevocation?.mode === 'replace') {
    credentialRevocationTitle = t(
      'Emergency replace {{provider}} credentials?',
      { provider: pendingCredentialRevocationProviderLabel },
    );
    credentialRevocationConfirmText = t('Replace and revoke');
    if (pendingCredentialRevocationProvider === 'stripe') {
      credentialRevocationWarning = t(
        'Emergency action: all previously accepted Stripe webhook signing secrets stop validating in this system immediately, and every unfinished Stripe order moves to manual review. If a new whsec is entered, it is saved atomically; otherwise local Stripe webhooks are disabled. This does not revoke Stripe Dashboard API keys, cancel Checkout Sessions or subscriptions, or issue refunds.',
      );
    } else {
      credentialRevocationWarning = t(
        'The entered {{provider}} identifier and secret will be saved atomically. The current and previous credential generations are revoked immediately, and unfinished orders using them move to manual review.',
        { provider: pendingCredentialRevocationProviderLabel },
      );
    }
  } else if (pendingCredentialRevocation?.mode === 'stripe_disable') {
    credentialRevocationTitle = t('Disable Stripe webhooks immediately?');
    credentialRevocationConfirmText = t('Disable and revoke');
    credentialRevocationWarning = t(
      'Emergency action: all Stripe webhook signing secrets stop validating in this system immediately, local Stripe webhooks are disabled, and every unfinished Stripe order moves to manual review. Stripe Checkout Sessions and subscriptions are not canceled, no refunds are issued, and Stripe Dashboard credentials remain active until you change them there.',
    );
  } else if (pendingCredentialRevocation?.mode === 'stripe_disable_all') {
    credentialRevocationTitle = t(
      'Disable all Stripe credentials immediately?',
    );
    credentialRevocationConfirmText = t('Disable all and revoke');
    credentialRevocationWarning = t(
      'Emergency shutdown: the Stripe API credential and all webhook signing secrets are cleared only from this system, every unfinished Stripe order moves to manual review, and durable Stripe history is marked with a credential incident. This does not revoke the API key in Stripe, cancel Checkout Sessions or subscriptions, or issue refunds. Complete those actions separately in the Stripe Dashboard when required.',
    );
  } else if (pendingCredentialRevocation) {
    credentialRevocationWarning = t(
      'No replacement credentials are entered. This only revokes the active previous {{provider}} credential; the current credential stays unchanged. Unfinished orders bound to the previous credential move to manual review.',
      { provider: pendingCredentialRevocationProviderLabel },
    );
  }

  const paymentConfigVersion = normalizePaymentConfigVersion(
    inputs[PAYMENT_CONFIG_VERSION_KEY],
  );

  return (
    <>
      <Spin spinning={loading} size='large'>
        <Card style={{ marginTop: '10px' }}>
          {!complianceConfirmed ? (
            <Banner
              type='warning'
              title={t('需要确认合规声明')}
              description={
                <div className='flex flex-col gap-2'>
                  <span>
                    {t(
                      '确认前，支付、兑换码、订阅计划和推荐奖励功能将保持锁定。',
                    )}
                  </span>
                  <Button
                    type='warning'
                    theme='solid'
                    onClick={() => setComplianceVisible(true)}
                  >
                    {t('确认合规声明')}
                  </Button>
                  <span>
                    {t(
                      'Without compliance confirmation, Epay and XORPay can only revoke an active previous credential. Enter complete replacements in their gateway forms after confirming compliance.',
                    )}
                  </span>
                  <div className='flex flex-wrap gap-2'>
                    <Button
                      type='danger'
                      onClick={() =>
                        requestEmergencyCredentialRevocation('stripe')
                      }
                    >
                      {t('Stripe webhook')}: {t('Disable Stripe webhooks now')}
                    </Button>
                    <Button
                      type='danger'
                      theme='borderless'
                      onClick={() =>
                        requestEmergencyCredentialRevocation(
                          'stripe',
                          { state: 'none', options: {} },
                          'stripe_disable_all',
                        )
                      }
                    >
                      {t('Disable Stripe completely now')}
                    </Button>
                    <Button
                      type='danger'
                      disabled={
                        !inputs[
                          'payment_setting.epay_previous_credential_active'
                        ]
                      }
                      onClick={() =>
                        requestEmergencyCredentialRevocation('epay')
                      }
                    >
                      {t('易支付')}: {t('立即撤销旧凭据')}
                    </Button>
                    <Button
                      type='danger'
                      disabled={
                        !inputs[
                          'payment_setting.xorpay_previous_credential_active'
                        ]
                      }
                      onClick={() =>
                        requestEmergencyCredentialRevocation('xorpay')
                      }
                    >
                      {t('XORPay')}: {t('立即撤销旧凭据')}
                    </Button>
                  </div>
                </div>
              }
              closeIcon={null}
              style={{ marginBottom: 16 }}
              fullMode={false}
            />
          ) : (
            <Banner
              type='success'
              title={t('合规声明已确认')}
              description={t('确认时间：{{time}}，确认用户：#{{userId}}', {
                time: inputs['payment_setting.compliance_confirmed_at']
                  ? new Date(
                      inputs['payment_setting.compliance_confirmed_at'] * 1000,
                    ).toLocaleString()
                  : '-',
                userId:
                  inputs['payment_setting.compliance_confirmed_by'] || '-',
              })}
              closeIcon={null}
              style={{ marginBottom: 16 }}
              fullMode={false}
            />
          )}
          <Tabs
            type='card'
            defaultActiveKey='overview'
            contentStyle={{ paddingTop: 24 }}
          >
            <Tabs.TabPane tab={t('支付概览')} itemKey='overview'>
              <PaymentOverviewPanel />
            </Tabs.TabPane>
            <Tabs.TabPane
              tab={t('通用设置')}
              itemKey='general'
              disabled={!complianceConfirmed}
            >
              <SettingsGeneralPayment
                options={inputs}
                configVersion={paymentConfigVersion}
                refresh={refreshAfterSave}
                withPaymentVerification={withPaymentVerification}
                hideSectionTitle
              />
            </Tabs.TabPane>
            <Tabs.TabPane
              tab={t('易支付设置')}
              itemKey='epay'
              disabled={!complianceConfirmed}
            >
              <SettingsPaymentGateway
                options={inputs}
                configVersion={paymentConfigVersion}
                refresh={refreshAfterSave}
                withPaymentVerification={withPaymentVerification}
                requestEmergencyCredentialRevocation={
                  requestEmergencyCredentialRevocation
                }
                hideSectionTitle
              />
            </Tabs.TabPane>
            <Tabs.TabPane
              tab={t('Stripe 设置')}
              itemKey='stripe'
              disabled={!complianceConfirmed}
            >
              <SettingsPaymentGatewayStripe
                options={inputs}
                configVersion={paymentConfigVersion}
                refresh={refreshAfterSave}
                withPaymentVerification={withPaymentVerification}
                requestEmergencyCredentialRevocation={
                  requestEmergencyCredentialRevocation
                }
                hideSectionTitle
              />
              <StripeLegacyInventoryPanel
                withPaymentVerification={withPaymentVerification}
              />
            </Tabs.TabPane>
            <Tabs.TabPane
              tab='XORPay'
              itemKey='xorpay'
              disabled={!complianceConfirmed}
            >
              <SettingsPaymentGatewayXorPay
                options={inputs}
                configVersion={paymentConfigVersion}
                refresh={refreshAfterSave}
                withPaymentVerification={withPaymentVerification}
                requestEmergencyCredentialRevocation={
                  requestEmergencyCredentialRevocation
                }
                hideSectionTitle
              />
            </Tabs.TabPane>
            <Tabs.TabPane
              tab={t('Creem 设置')}
              itemKey='creem'
              disabled={!complianceConfirmed}
            >
              <SettingsPaymentGatewayCreem
                options={inputs}
                configVersion={paymentConfigVersion}
                refresh={refreshAfterSave}
                withPaymentVerification={withPaymentVerification}
                hideSectionTitle
              />
            </Tabs.TabPane>
            <Tabs.TabPane
              tab={t('Waffo Pancake 设置')}
              itemKey='waffo-pancake'
              disabled={!complianceConfirmed}
            >
              <SettingsPaymentGatewayWaffoPancake
                options={inputs}
                configVersion={paymentConfigVersion}
                refresh={refreshAfterSave}
                withPaymentVerification={withPaymentVerification}
                hideSectionTitle
              />
            </Tabs.TabPane>
            <Tabs.TabPane
              tab={t('Waffo 设置')}
              itemKey='waffo'
              disabled={!complianceConfirmed}
            >
              <SettingsPaymentGatewayWaffo
                options={inputs}
                configVersion={paymentConfigVersion}
                refresh={refreshAfterSave}
                withPaymentVerification={withPaymentVerification}
                hideSectionTitle
              />
            </Tabs.TabPane>
            <Tabs.TabPane
              tab={t('支付限额')}
              itemKey='limits'
              disabled={!complianceConfirmed}
            >
              <PaymentLimitsPanel
                withPaymentVerification={withPaymentVerification}
              />
            </Tabs.TabPane>
          </Tabs>
        </Card>
        <Modal
          visible={pendingCredentialRevocation !== null}
          title={credentialRevocationTitle}
          centered
          confirmLoading={loading}
          okType='danger'
          okText={credentialRevocationConfirmText}
          cancelText={t('取消')}
          okButtonProps={{ disabled: !credentialRevocationReasonValid }}
          onOk={confirmEmergencyCredentialRevocation}
          onCancel={() => {
            if (loading) return;
            setPendingCredentialRevocation(null);
            setCredentialRevocationReason('');
          }}
        >
          <div className='flex flex-col gap-3'>
            <p>{credentialRevocationWarning}</p>
            <label
              className='text-sm font-medium'
              htmlFor='classic-emergency-credential-revocation-reason'
            >
              {t('Emergency revocation reason')}
            </label>
            <TextArea
              id='classic-emergency-credential-revocation-reason'
              value={credentialRevocationReason}
              maxCount={512}
              autosize={{ minRows: 3, maxRows: 6 }}
              disabled={loading}
              placeholder={t(
                'Describe the credential leak, compromise, or emergency rotation',
              )}
              onChange={setCredentialRevocationReason}
            />
            <div className='flex flex-wrap justify-between gap-2 text-xs'>
              <span
                className={
                  credentialRevocationReason.length > 0 &&
                  !credentialRevocationReasonValid
                    ? 'text-red-500'
                    : 'text-gray-500'
                }
              >
                {credentialRevocationReason.length > 0 &&
                !credentialRevocationReasonValid
                  ? t('Reason must be between 8 and 512 characters')
                  : t(
                      'Enter 8 to 512 characters explaining the credential incident and response.',
                    )}
              </span>
              <span className='text-gray-500 tabular-nums'>
                {credentialRevocationReason.length} / 512
              </span>
            </div>
          </div>
        </Modal>
        <RiskAcknowledgementModal
          visible={complianceVisible}
          title={t('确认合规声明')}
          markdownContent={t(
            '该操作将启用支付、兑换码、订阅计划和推荐奖励相关功能。请仔细阅读并确认以下声明。',
          )}
          checklist={complianceStatements}
          inputPrompt={t('请输入以下文字以确认:')}
          requiredText={requiredComplianceText}
          requiredTextParts={requiredComplianceTextParts}
          inputPlaceholder={t('请输入确认文案')}
          mismatchText={t('输入内容与要求文案不一致')}
          cancelText={t('取消')}
          confirmText={t('确认并启用')}
          onCancel={() => setComplianceVisible(false)}
          onConfirm={confirmCompliance}
        />
        <SecureVerificationModal
          visible={paymentVerificationVisible}
          verificationMethods={paymentVerificationMethods}
          verificationState={paymentVerificationState}
          onVerify={(method, code) =>
            executePaymentVerification(method, code).catch(() => {
              // useSecureVerification already reports the failure to the user.
            })
          }
          onCancel={cancelPaymentVerification}
          onCodeChange={setPaymentVerificationCode}
          onMethodSwitch={switchPaymentVerificationMethod}
          title={paymentVerificationState.title}
          description={paymentVerificationState.description}
        />
      </Spin>
    </>
  );
};

export default PaymentSetting;
