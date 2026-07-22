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

import React, { useEffect, useRef, useState } from 'react';
import { Banner, Button, Col, Form, Row, Spin } from '@douyinfe/semi-ui';
import { ShieldCheck } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import {
  API,
  removeTrailingSlash,
  showError,
  showSuccess,
  showWarning,
} from '../../../helpers';
import { buildEmergencyCredentialReplacement } from '../../../helpers/payment-credential-revocation';
import {
  createPaymentAdminError,
  getPaymentAdminErrorMessage,
} from '../../../helpers/payment-admin-errors';

export default function SettingsPaymentGatewayXorPay(props) {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);
  const [inputs, setInputs] = useState({
    XorPayAid: '',
    XorPayAppSecret: '',
    XorPayUnitPrice: 7.3,
    XorPayMinTopUp: 1,
    XorPayEnabledMethods: [],
  });
  const formApiRef = useRef(null);
  const submitInFlightRef = useRef(false);

  useEffect(() => {
    if (!props.options || !formApiRef.current) return;
    const next = {
      XorPayAid: props.options.XorPayAid || '',
      XorPayAppSecret: '',
      XorPayUnitPrice: Number(props.options.XorPayUnitPrice || 7.3),
      XorPayMinTopUp: Number(props.options.XorPayMinTopUp || 1),
      XorPayEnabledMethods: Array.isArray(props.options.XorPayEnabledMethods)
        ? props.options.XorPayEnabledMethods
        : [],
    };
    setInputs(next);
    formApiRef.current.setValues(next);
  }, [props.options]);

  const emergencyReplacement = buildEmergencyCredentialReplacement('xorpay', {
    identifier: inputs.XorPayAid,
    savedIdentifier: props.options.XorPayAid,
    secret: inputs.XorPayAppSecret,
  });
  const previousCredentialActive = Boolean(
    props.options['payment_setting.xorpay_previous_credential_active'],
  );
  const incompleteReplacement = emergencyReplacement.state === 'partial';
  const previousCredentialUnavailable =
    emergencyReplacement.state === 'none' && !previousCredentialActive;
  let emergencyDescription = t(
    'No replacement credentials are entered. This only revokes the active previous {{provider}} credential; the current credential stays unchanged. Unfinished orders bound to the previous credential move to manual review.',
    { provider: 'XORPay' },
  );
  let emergencyActionLabel = t('立即撤销旧凭据');
  if (emergencyReplacement.state === 'complete') {
    emergencyDescription = t(
      'The entered {{provider}} identifier and secret will be saved atomically. The current and previous credential generations are revoked immediately, and unfinished orders using them move to manual review.',
      { provider: 'XORPay' },
    );
    emergencyActionLabel = t('Emergency replace credentials');
  } else if (incompleteReplacement) {
    emergencyDescription = t(
      'The replacement credential pair is incomplete. Enter both the identifier and secret, or restore the saved identifier before using this emergency action.',
    );
  } else if (previousCredentialUnavailable) {
    emergencyDescription = t(
      'No active previous {{provider}} credential is available to revoke. Enter a complete replacement identifier and secret to perform an emergency replacement.',
      { provider: 'XORPay' },
    );
    emergencyActionLabel = t('No previous credential to revoke');
  }

  const save = async () => {
    if (submitInFlightRef.current) return;

    if (!props.options.CustomCallbackAddress) {
      showError(t('请先在支付通用设置中填写并保存支付回调安全基址'));
      return;
    }
    const aid = (inputs.XorPayAid || '').trim();
    if (!/^[A-Za-z0-9_-]{1,128}$/.test(aid)) {
      showError(t('XORPay 商户 AID 格式无效'));
      return;
    }
    const minTopUp = Number(inputs.XorPayMinTopUp);
    if (!Number.isInteger(minTopUp) || minTopUp < 1 || minTopUp > 10000) {
      showError(t('最低充值数量必须是 1 到 10000 之间的整数'));
      return;
    }
    const unitPrice = Number(inputs.XorPayUnitPrice);
    if (!Number.isFinite(unitPrice) || unitPrice <= 0) {
      showError(t('充值价格必须是大于 0 的数字'));
      return;
    }
    submitInFlightRef.current = true;
    setLoading(true);
    try {
      const options = {
        XorPayAid: aid,
        XorPayUnitPrice: unitPrice,
        XorPayMinTopUp: minTopUp,
        XorPayEnabledMethods: JSON.stringify(inputs.XorPayEnabledMethods || []),
      };
      if ((inputs.XorPayAppSecret || '').trim()) {
        options.XorPayAppSecret = inputs.XorPayAppSecret.trim();
      }
      await props.withPaymentVerification(async () => {
        const response = await API.put(
          '/api/option/payment',
          {
            options,
            expected_version: props.configVersion || 1,
          },
          { skipErrorHandler: true },
        );
        if (!response.data?.success) {
          throw createPaymentAdminError(response.data, t('更新失败'));
        }
        const nextInputs = { ...inputs, XorPayAppSecret: '' };
        setInputs(nextInputs);
        formApiRef.current?.setValues(nextInputs);
        const refreshed = await props.refresh?.(response.data?.data?.version);
        if (refreshed === false) {
          showWarning(t('设置已保存，但最新状态刷新失败'));
        } else {
          showSuccess(t('更新成功'));
        }
        return response;
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
        onValueChange={setInputs}
        getFormApi={(api) => (formApiRef.current = api)}
      >
        <Form.Section
          text={props.hideSectionTitle ? undefined : t('XORPay 设置')}
        >
          <Banner
            type='info'
            icon={<ShieldCheck size={16} />}
            description={t(
              'XORPay API 地址由服务端固定，密钥不会回显。当前支持微信 Native 扫码、支付宝当面付，以及显式启用后的微信 JSAPI。',
            )}
            closeIcon={null}
            style={{ marginBottom: 16 }}
          />
          <Row gutter={16}>
            <Col xs={24} md={12}>
              <Form.Input
                field='XorPayAid'
                label={t('商户 AID')}
                placeholder={t('请输入 XORPay 商户 AID')}
              />
            </Col>
            <Col xs={24} md={12}>
              <Form.Input
                field='XorPayAppSecret'
                type='password'
                label={t('应用密钥')}
                placeholder={t('留空表示保持当前密钥')}
                autoComplete='new-password'
              />
            </Col>
          </Row>
          <Row gutter={16} style={{ marginTop: 16 }}>
            <Col xs={24} md={12}>
              <Form.InputNumber
                field='XorPayUnitPrice'
                min={0.01}
                precision={2}
                label={t('充值价格（本地货币/美元）')}
              />
            </Col>
            <Col xs={24} md={12}>
              <Form.InputNumber
                field='XorPayMinTopUp'
                min={1}
                max={10000}
                precision={0}
                label={t('最低充值美元数量')}
              />
            </Col>
          </Row>
          <Form.CheckboxGroup
            field='XorPayEnabledMethods'
            label={t('启用支付方式')}
            options={[
              { label: t('微信 Native 扫码'), value: 'native' },
              { label: t('支付宝当面付'), value: 'alipay' },
              {
                label: t('微信 JSAPI（仅微信内浏览器）'),
                value: 'jsapi',
              },
            ]}
            style={{ marginTop: 16 }}
          />
          <Banner
            type='warning'
            title={t('微信 JSAPI 必须显式启用')}
            description={t(
              '仅在 XORPay 商户已开通 JSAPI、已配置精确的 HTTPS 支付目录，并完成所需的网站或 ICP 备案后启用。该方式仅适用于微信内置浏览器。前端调用成功不代表支付成功，最终结果只能以服务端验签回调或本地订单确认为准。此能力尚未使用真实商户完成验证。',
            )}
            closeIcon={null}
            style={{ marginTop: 12 }}
          />
          <div className='mt-4 text-sm text-gray-500'>
            {t('回调地址')}：
            <code>
              {props.options.CustomCallbackAddress
                ? `${removeTrailingSlash(
                    props.options.CustomCallbackAddress,
                  )}/api/xorpay/notify`
                : '<CallbackAddress>/api/xorpay/notify'}
            </code>
          </div>
          <Button onClick={save} loading={loading} style={{ marginTop: 16 }}>
            {t('更新 XORPay 设置')}
          </Button>
          <div style={{ marginTop: 16 }}>
            <Banner
              type='warning'
              title={
                emergencyReplacement.state === 'complete'
                  ? t('Emergency replace credentials')
                  : t('紧急撤销旧凭据')
              }
              description={emergencyDescription}
              closeIcon={null}
            />
            <Button
              theme='solid'
              type='danger'
              disabled={
                loading ||
                incompleteReplacement ||
                previousCredentialUnavailable
              }
              onClick={() =>
                props.requestEmergencyCredentialRevocation?.(
                  'xorpay',
                  emergencyReplacement,
                )
              }
              style={{ marginTop: 8 }}
            >
              {emergencyActionLabel}
            </Button>
          </div>
        </Form.Section>
      </Form>
    </Spin>
  );
}
