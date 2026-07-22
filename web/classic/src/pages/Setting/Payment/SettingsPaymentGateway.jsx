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
import { Banner, Button, Form, Row, Col, Spin } from '@douyinfe/semi-ui';
import {
  API,
  removeTrailingSlash,
  showError,
  showSuccess,
  showWarning,
} from '../../../helpers';
import { useTranslation } from 'react-i18next';
import { Info } from 'lucide-react';
import { buildEmergencyCredentialReplacement } from '../../../helpers/payment-credential-revocation';
import {
  createPaymentAdminError,
  getPaymentAdminErrorMessage,
} from '../../../helpers/payment-admin-errors';

export default function SettingsPaymentGateway(props) {
  const { t } = useTranslation();
  const sectionTitle = props.hideSectionTitle ? undefined : t('易支付设置');
  const [loading, setLoading] = useState(false);
  const [inputs, setInputs] = useState({
    PayAddress: '',
    EpayId: '',
    EpayKey: '',
    Price: 7.3,
    MinTopUp: 1,
  });
  const formApiRef = useRef(null);
  const submitInFlightRef = useRef(false);

  useEffect(() => {
    if (props.options && formApiRef.current) {
      const currentInputs = {
        PayAddress: props.options.PayAddress || '',
        EpayId: props.options.EpayId || '',
        EpayKey: props.options.EpayKey || '',
        Price:
          props.options.Price !== undefined
            ? parseFloat(props.options.Price)
            : 7.3,
        MinTopUp:
          props.options.MinTopUp !== undefined
            ? parseFloat(props.options.MinTopUp)
            : 1,
      };

      setInputs(currentInputs);
      formApiRef.current.setValues(currentInputs);
    }
  }, [props.options]);

  const handleFormChange = (values) => {
    setInputs(values);
  };

  const emergencyReplacement = buildEmergencyCredentialReplacement('epay', {
    identifier: inputs.EpayId,
    savedIdentifier: props.options.EpayId,
    secret: inputs.EpayKey,
  });
  const previousCredentialActive = Boolean(
    props.options['payment_setting.epay_previous_credential_active'],
  );
  const incompleteReplacement = emergencyReplacement.state === 'partial';
  const previousCredentialUnavailable =
    emergencyReplacement.state === 'none' && !previousCredentialActive;
  let emergencyDescription = t(
    'No replacement credentials are entered. This only revokes the active previous {{provider}} credential; the current credential stays unchanged. Unfinished orders bound to the previous credential move to manual review.',
    { provider: 'Epay' },
  );
  let emergencyActionLabel = t('立即撤销旧凭据');
  if (emergencyReplacement.state === 'complete') {
    emergencyDescription = t(
      'The entered {{provider}} identifier and secret will be saved atomically. The current and previous credential generations are revoked immediately, and unfinished orders using them move to manual review.',
      { provider: 'Epay' },
    );
    emergencyActionLabel = t('Emergency replace credentials');
  } else if (incompleteReplacement) {
    emergencyDescription = t(
      'The replacement credential pair is incomplete. Enter both the identifier and secret, or restore the saved identifier before using this emergency action.',
    );
  } else if (previousCredentialUnavailable) {
    emergencyDescription = t(
      'No active previous {{provider}} credential is available to revoke. Enter a complete replacement identifier and secret to perform an emergency replacement.',
      { provider: 'Epay' },
    );
    emergencyActionLabel = t('No previous credential to revoke');
  }

  const submitPayAddress = async () => {
    if (submitInFlightRef.current) return;

    if (!props.options.CustomCallbackAddress) {
      showError(t('请先在支付通用设置中填写并保存支付回调安全基址'));
      return;
    }
    const minTopUp = Number(inputs.MinTopUp);
    if (!Number.isInteger(minTopUp) || minTopUp < 1 || minTopUp > 10000) {
      showError(t('最低充值数量必须是 1 到 10000 之间的整数'));
      return;
    }
    const unitPrice = Number(inputs.Price);
    if (!Number.isFinite(unitPrice) || unitPrice <= 0) {
      showError(t('充值价格必须是大于 0 的数字'));
      return;
    }

    submitInFlightRef.current = true;
    setLoading(true);
    try {
      const options = [
        { key: 'PayAddress', value: removeTrailingSlash(inputs.PayAddress) },
      ];

      if (inputs.EpayId !== '') {
        options.push({ key: 'EpayId', value: inputs.EpayId });
      }
      if (inputs.EpayKey !== undefined && inputs.EpayKey !== '') {
        options.push({ key: 'EpayKey', value: inputs.EpayKey });
      }
      if (inputs.Price !== '') {
        options.push({ key: 'Price', value: unitPrice });
      }
      if (inputs.MinTopUp !== '') {
        options.push({ key: 'MinTopUp', value: minTopUp });
      }

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
          const nextInputs = { ...inputs, EpayKey: '' };
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
            icon={<Info size={16} />}
            description={
              <div className='space-y-1'>
                <div>
                  {t('当前仅支持易支付接口，回调地址请在通用设置中配置。')}
                </div>
                <div>
                  {t('回调地址')}：
                  <code>
                    {props.options.CustomCallbackAddress
                      ? `${removeTrailingSlash(
                          props.options.CustomCallbackAddress,
                        )}/api/payment/epay/notify`
                      : '<CallbackAddress>/api/payment/epay/notify'}
                  </code>
                </div>
              </div>
            }
            style={{ marginBottom: 16 }}
          />
          <Row gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.Input
                field='PayAddress'
                label={t('支付地址')}
                placeholder={t('例如：https://yourdomain.com')}
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.Input
                field='EpayId'
                label={t('商户 ID')}
                placeholder={t('例如：0001')}
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.Input
                field='EpayKey'
                label={t('API 密钥')}
                placeholder={t('敏感信息不会发送到前端显示')}
                type='password'
                autoComplete='new-password'
              />
            </Col>
          </Row>
          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.InputNumber
                field='Price'
                precision={2}
                label={t('充值价格（x元/美金）')}
                placeholder={t('例如：7，就是7元/美金')}
              />
            </Col>
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.InputNumber
                field='MinTopUp'
                min={1}
                max={10000}
                precision={0}
                label={t('最低充值美元数量')}
                placeholder={t('例如：2，就是最低充值2$')}
              />
            </Col>
          </Row>
          <Button
            onClick={submitPayAddress}
            loading={loading}
            style={{ marginTop: 16 }}
          >
            {t('更新易支付设置')}
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
                  'epay',
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
