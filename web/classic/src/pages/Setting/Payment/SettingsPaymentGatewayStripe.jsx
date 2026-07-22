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

import React, { useEffect, useState, useRef } from 'react';
import { Banner, Button, Col, Form, Row, Spin } from '@douyinfe/semi-ui';
import {
  API,
  removeTrailingSlash,
  showError,
  showSuccess,
  showWarning,
} from '../../../helpers';
import { useTranslation } from 'react-i18next';
import { BookOpen, TriangleAlert } from 'lucide-react';
import { buildEmergencyCredentialReplacement } from '../../../helpers/payment-credential-revocation';
import { resolveStripeTestModeNotice } from '../../../helpers/stripe-test-mode-readiness';
import {
  createPaymentAdminError,
  getPaymentAdminErrorMessage,
} from '../../../helpers/payment-admin-errors';
import { buildStripeCheckoutAllowedHostsUpdate } from './payment-admin-utils';
import { resolveStripeWebhookContract } from './stripe-webhook-contract';

export default function SettingsPaymentGateway(props) {
  const { t } = useTranslation();
  const sectionTitle = props.hideSectionTitle ? undefined : t('Stripe 设置');
  const [loading, setLoading] = useState(false);
  const [inputs, setInputs] = useState({
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
  });
  const formApiRef = useRef(null);
  const submitInFlightRef = useRef(false);

  useEffect(() => {
    if (props.options && formApiRef.current) {
      const currentInputs = {
        StripeApiSecret: props.options.StripeApiSecret || '',
        StripeWebhookSecret: props.options.StripeWebhookSecret || '',
        StripePriceId: props.options.StripePriceId || '',
        StripeAccountId: props.options.StripeAccountId || '',
        StripeCheckoutAllowedHosts:
          props.options.StripeCheckoutAllowedHosts || '',
        StripeCredentialAccountId:
          props.options.StripeCredentialAccountId || '',
        StripeCredentialLivemode: props.options.StripeCredentialLivemode || '',
        StripeUnitPrice:
          props.options.StripeUnitPrice !== undefined
            ? parseFloat(props.options.StripeUnitPrice)
            : 8.0,
        StripeMinTopUp:
          props.options.StripeMinTopUp !== undefined
            ? parseFloat(props.options.StripeMinTopUp)
            : 1,
        StripeCurrency: (props.options.StripeCurrency || 'USD').toUpperCase(),
      };
      setInputs(currentInputs);
      formApiRef.current.setValues(currentInputs);
    }
  }, [props.options]);

  const handleFormChange = (values) => {
    setInputs(values);
  };

  const emergencyReplacement = buildEmergencyCredentialReplacement('stripe', {
    secret: inputs.StripeWebhookSecret,
  });
  const stripeTestModeNotice = resolveStripeTestModeNotice({
    credentialLivemode: props.options?.StripeCredentialLivemode,
    enabled: props.options?.['payment_setting.stripe_test_mode_enabled'],
    blocked: props.options?.['payment_setting.stripe_test_mode_blocked'],
    isolationRequired:
      props.options?.['payment_setting.stripe_test_mode_isolation_required'],
  });
  const stripeWebhookContract = resolveStripeWebhookContract(
    props.options?.['payment_setting.stripe_webhook_api_version'],
    props.options?.['payment_setting.stripe_webhook_secret_overlap_hours'],
  );

  const submitStripeSetting = async () => {
    if (submitInFlightRef.current) return;

    if (!props.options.CustomCallbackAddress) {
      showError(t('请先在支付通用设置中填写并保存支付回调安全基址'));
      return;
    }
    const minTopUp = Number(inputs.StripeMinTopUp);
    if (!Number.isInteger(minTopUp) || minTopUp < 1 || minTopUp > 10000) {
      showError(t('最低充值数量必须是 1 到 10000 之间的整数'));
      return;
    }
    const unitPrice = Number(inputs.StripeUnitPrice);
    if (!Number.isFinite(unitPrice) || unitPrice <= 0) {
      showError(t('充值价格必须是大于 0 的数字'));
      return;
    }
    const currency = (inputs.StripeCurrency || '').trim().toUpperCase();
    if (!/^[A-Z]{3}$/.test(currency)) {
      showError(t('Stripe 货币必须是三位 ISO 4217 代码'));
      return;
    }

    submitInFlightRef.current = true;
    setLoading(true);
    try {
      const options = [];

      // Stripe credential identity fields are resolved by the backend from the
      // API credential. They are intentionally never included in this payload.

      if (inputs.StripeApiSecret && inputs.StripeApiSecret !== '') {
        options.push({ key: 'StripeApiSecret', value: inputs.StripeApiSecret });
      }
      if (inputs.StripeWebhookSecret && inputs.StripeWebhookSecret !== '') {
        options.push({
          key: 'StripeWebhookSecret',
          value: inputs.StripeWebhookSecret,
        });
      }
      options.push({
        key: 'StripePriceId',
        value: (inputs.StripePriceId || '').trim(),
      });
      options.push({
        key: 'StripeAccountId',
        value: (inputs.StripeAccountId || '').trim(),
      });
      options.push(
        buildStripeCheckoutAllowedHostsUpdate(
          inputs.StripeCheckoutAllowedHosts,
        ),
      );
      if (
        inputs.StripeUnitPrice !== undefined &&
        inputs.StripeUnitPrice !== null
      ) {
        options.push({
          key: 'StripeUnitPrice',
          value: unitPrice,
        });
      }
      if (
        inputs.StripeMinTopUp !== undefined &&
        inputs.StripeMinTopUp !== null
      ) {
        options.push({
          key: 'StripeMinTopUp',
          value: minTopUp,
        });
      }
      options.push({ key: 'StripeCurrency', value: currency });
      await props.withPaymentVerification(async () => {
        const result = await API.put(
          '/api/option/payment',
          {
            options: Object.fromEntries(
              options.map((item) => [item.key, item.value]),
            ),
            expected_version: props.configVersion || 1,
          },
          { skipErrorHandler: true },
        );
        if (result.data?.success) {
          const nextInputs = {
            ...inputs,
            StripeApiSecret: '',
            StripeWebhookSecret: '',
            StripeCurrency: currency,
          };
          setInputs(nextInputs);
          formApiRef.current?.setValues(nextInputs);
          const refreshed = await props.refresh?.(result.data?.data?.version);
          if (refreshed === false) {
            showWarning(t('设置已保存，但最新状态刷新失败'));
          } else {
            showSuccess(t('更新成功'));
          }
        } else {
          throw createPaymentAdminError(result.data, t('更新失败'));
        }
        return result;
      });
    } catch (error) {
      showError(getPaymentAdminErrorMessage(error, t, t('更新失败')));
    } finally {
      submitInFlightRef.current = false;
      setLoading(false);
    }
  };

  return (
    <Spin spinning={loading}>
      <Form
        initValues={inputs}
        onValueChange={handleFormChange}
        getFormApi={(api) => (formApiRef.current = api)}
      >
        <Form.Section text={sectionTitle}>
          <Banner
            type='info'
            icon={<BookOpen size={16} />}
            title={t('Current Stripe connection uses one-time Checkout')}
            description={
              <>
                {t(
                  'Unified top-ups and fixed-term access purchases create Checkout Sessions in payment mode. They do not create auto-renewing subscriptions. Any legacy recurring subscription inventory is shown separately in Payment Operations.',
                )}
                <br />
                {t('Stripe 密钥、Webhook 等设置请')}
                <a
                  href='https://dashboard.stripe.com/developers'
                  target='_blank'
                  rel='noreferrer'
                >
                  {t('点击此处')}
                </a>
                {t('进行设置，建议先在')}
                <a
                  href='https://dashboard.stripe.com/test/developers'
                  target='_blank'
                  rel='noreferrer'
                >
                  {t('测试环境')}
                </a>
                {t('完成联调。')}
                <br />
                {t('回调地址')}：
                {props.options.CustomCallbackAddress
                  ? removeTrailingSlash(props.options.CustomCallbackAddress)
                  : '<CallbackAddress>'}
                /api/stripe/webhook
              </>
            }
            style={{ marginBottom: 12 }}
          />
          <Banner
            type='warning'
            icon={<TriangleAlert size={16} />}
            description={
              <div className='flex flex-col gap-1'>
                <span>
                  {t('Required for current one-time Checkout:')}{' '}
                  <code>
                    {t(
                      'checkout.session.completed, checkout.session.async_payment_succeeded, checkout.session.async_payment_failed, checkout.session.expired, charge.refunded, charge.dispute.created, charge.dispute.closed',
                    )}
                  </code>
                </span>
                <span>
                  {stripeWebhookContract.apiVersion ? (
                    <>
                      {t('Endpoint API version:')}{' '}
                      <code>{stripeWebhookContract.apiVersion}</code>{' '}
                      {t(
                        'Configure this endpoint in Stripe Workbench with the same API version as the server SDK so Stripe sends the payload shape this release validates.',
                      )}
                    </>
                  ) : (
                    t(
                      'The server did not report its Stripe webhook API version. Refresh after all application nodes run the same release before configuring the endpoint.',
                    )
                  )}
                </span>
                <span>
                  {t(
                    'If legacy recurring inventory exists, also enable customer.subscription.* and invoice.* events. Use "Sync from Stripe" in Payment Operations to refresh observations; scheduling cancellation at period end is a separate verified action.',
                  )}
                </span>
              </div>
            }
            style={{ marginBottom: 16 }}
          />
          <Banner
            type='info'
            icon={<BookOpen size={16} />}
            title={t('Normal Stripe webhook secret rotation')}
            description={
              <div className='flex flex-col gap-1'>
                <span>
                  {t(
                    'First choose Roll secret for this endpoint in Stripe Workbench and copy the new signing secret, then save it in this system. This system keeps the previous secret for {{hours}} hours so delayed webhooks can still be verified, and normal rotation is blocked during that overlap. After the new secret is stable, let the previous secret expire automatically. Use emergency revocation only if you suspect compromise.',
                    { hours: stripeWebhookContract.overlapHours },
                  )}
                </span>
                {props.options?.[
                  'payment_setting.stripe_previous_credential_active'
                ] ? (
                  <strong>
                    {t(
                      'A previous signing secret is still within the overlap window of {{hours}} hours. Do not roll or save another normal replacement until it expires.',
                      { hours: stripeWebhookContract.overlapHours },
                    )}
                  </strong>
                ) : null}
              </div>
            }
            closeIcon={null}
            style={{ marginBottom: 16 }}
          />
          {stripeTestModeNotice?.state === 'blocked' && (
            <Banner
              type='danger'
              icon={<TriangleAlert size={16} />}
              title={t('Stripe 测试模式已阻止')}
              description={t(
                '当前保存的是 Stripe 测试凭证，但 PAYMENT_STRIPE_TEST_MODE_ENABLED 未开启。新的 Stripe 支付和额度入账均会被阻止。仅可在与生产数据完全隔离的非生产环境中开启后重启服务。',
              )}
              closeIcon={null}
              style={{ marginBottom: 16 }}
            />
          )}
          {stripeTestModeNotice?.state === 'enabled' && (
            <Banner
              type='warning'
              icon={<TriangleAlert size={16} />}
              title={t('Stripe 测试模式可以为账户增加额度')}
              description={
                stripeTestModeNotice.isolationRequired
                  ? t(
                      'Stripe 测试模式已开启。测试卡可以完成支付，并为此数据库中的真实用户增加额度。必须使用完全隔离的数据库和用户账户，绝不能连接生产数据。',
                    )
                  : t(
                      'Stripe 测试模式已开启。测试卡可以完成支付，并为此数据库中的用户增加额度。',
                    )
              }
              closeIcon={null}
              style={{ marginBottom: 16 }}
            />
          )}
          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginBottom: 16 }}
          >
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.Input
                field='StripeCredentialAccountId'
                label={t('Stripe 凭证账户身份')}
                placeholder={t('保存 Stripe 设置后由服务端自动验证并绑定')}
                extraText={t(
                  '该字段由 Stripe API 密钥验证结果生成，仅供查看，不能手动修改。',
                )}
                disabled
                readOnly
              />
            </Col>
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.Input
                field='StripeCredentialLivemode'
                label={t('Stripe 凭证环境')}
                placeholder={t('保存 Stripe 设置后由服务端自动验证并绑定')}
                extraText={t(
                  '该字段由 Stripe API 密钥验证结果生成。存在 Stripe 支付历史后，测试与正式环境不能互相切换。',
                )}
                disabled
                readOnly
              />
            </Col>
          </Row>
          <Row gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}>
            <Col xs={24} sm={24} md={6} lg={6} xl={6}>
              <Form.Input
                field='StripeApiSecret'
                label={t('API 密钥')}
                placeholder={t('例如：sk_xxx 或 rk_xxx，留空表示保持当前不变')}
                extraText={t(
                  '保存后不会回显，请填写当前环境对应的 Stripe API 密钥',
                )}
                type='password'
                autoComplete='new-password'
              />
            </Col>
            <Col xs={24} sm={24} md={6} lg={6} xl={6}>
              <Form.Input
                field='StripeWebhookSecret'
                label={t('Webhook 签名密钥')}
                placeholder={t('例如：whsec_xxx，留空表示保持当前不变')}
                extraText={t('用于校验 Stripe Webhook 签名，保存后不会回显')}
                type='password'
                autoComplete='new-password'
              />
            </Col>
            <Col xs={24} sm={24} md={6} lg={6} xl={6}>
              <Form.Input
                field='StripePriceId'
                label={t('One-time Checkout product template Price ID')}
                placeholder={t('例如：price_xxx')}
                extraText={t(
                  'Used only as a product and currency template for server-quoted one-time Checkout. It is not a recurring subscription price.',
                )}
                autoComplete='off'
              />
            </Col>
            <Col xs={24} sm={24} md={6} lg={6} xl={6}>
              <Form.Input
                field='StripeAccountId'
                label={t('Connected Account ID')}
                placeholder='acct_...'
                extraText={t(
                  'Optional Stripe Connect account. Leave blank for the platform account.',
                )}
                autoComplete='off'
              />
            </Col>
          </Row>
          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col span={24}>
              <Form.TextArea
                field='StripeCheckoutAllowedHosts'
                label={t('Custom Checkout domain allowlist')}
                placeholder='pay.example.com'
                rows={3}
                maxLength={4096}
                extraText={t(
                  'Optional. Enter exact hostnames only, one per line or separated by commas. Stripe-owned *.stripe.com hosts are always allowed. Wildcards, URLs, ports, IP addresses, localhost, and credentials are rejected.',
                )}
              />
            </Col>
          </Row>
          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24} sm={24} md={6} lg={6} xl={6}>
              <Form.InputNumber
                field='StripeUnitPrice'
                min={0.01}
                precision={2}
                label={t('充值价格（结算货币/美元）')}
                placeholder={t(
                  '例如：8，表示每 1 美元额度支付 8 个结算货币单位',
                )}
                extraText={t(
                  'Positive multiplier that converts the USD base price into the Stripe settlement currency for wallet top-ups and fixed-term purchases.',
                )}
              />
            </Col>
            <Col xs={24} sm={24} md={6} lg={6} xl={6}>
              <Form.InputNumber
                field='StripeMinTopUp'
                min={1}
                max={10000}
                precision={0}
                label={t('最低充值美元数量')}
                placeholder={t('例如：2，就是最低充值2$')}
                extraText={t('用户单次最少可充值的美元数量')}
              />
            </Col>
            <Col xs={24} sm={24} md={6} lg={6} xl={6}>
              <Form.Input
                field='StripeCurrency'
                label={t('Stripe 结算货币')}
                placeholder='USD'
                maxLength={3}
                extraText={t(
                  'Enter a three-letter ISO 4217 code such as USD, EUR, or JPY. This controls new one-time Checkout orders and does not rewrite legacy subscription history.',
                )}
              />
            </Col>
            <Col xs={24} sm={24} md={6} lg={6} xl={6}>
              <Banner
                type='warning'
                icon={<TriangleAlert size={16} />}
                description={t(
                  'To keep server quotes consistent with webhook amounts, unified payments do not support Stripe promotion codes.',
                )}
              />
            </Col>
          </Row>
          <Banner
            type='danger'
            title={
              emergencyReplacement.state === 'complete'
                ? t('Emergency replace credentials')
                : t('Disable Stripe webhooks now')
            }
            description={
              <div className='flex flex-col items-start gap-3'>
                <span>
                  {t(
                    'Emergency action: all previously accepted Stripe webhook signing secrets stop validating immediately, and every unfinished Stripe order moves to manual review. If a new whsec is entered in this form, it is saved atomically; otherwise Stripe webhooks are disabled. Clearing or normally rotating a secret does not perform this emergency revocation.',
                  )}
                </span>
                <span>
                  {t(
                    'This local action does not revoke a Stripe Dashboard API key, cancel Checkout Sessions or subscriptions, or issue refunds. Complete those actions separately in Stripe when required.',
                  )}
                </span>
                <Button
                  type='danger'
                  onClick={() =>
                    props.requestEmergencyCredentialRevocation?.(
                      'stripe',
                      emergencyReplacement,
                    )
                  }
                >
                  {emergencyReplacement.state === 'complete'
                    ? t('Emergency replace credentials')
                    : t('Disable Stripe webhooks now')}
                </Button>
                <Button
                  type='danger'
                  theme='borderless'
                  onClick={() =>
                    props.requestEmergencyCredentialRevocation?.(
                      'stripe',
                      { state: 'none', options: {} },
                      'stripe_disable_all',
                    )
                  }
                >
                  {t('Disable Stripe completely now')}
                </Button>
              </div>
            }
            closeIcon={null}
            fullMode={false}
            style={{ marginBottom: 16 }}
          />
          <Button onClick={submitStripeSetting} loading={loading}>
            {t('更新 Stripe 设置')}
          </Button>
        </Form.Section>
      </Form>
    </Spin>
  );
}
