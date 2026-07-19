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
import { Button, Col, Form, Row, Spin } from '@douyinfe/semi-ui';
import {
  API,
  removeTrailingSlash,
  showError,
  showSuccess,
  verifyJSON,
} from '../../../helpers';
import { useTranslation } from 'react-i18next';

const isSecurePaymentCallbackOrigin = (value) => {
  const trimmed = (value || '').trim();
  if (!trimmed) return false;
  try {
    const parsed = new URL(trimmed);
    const loopback = ['localhost', '127.0.0.1', '::1', '[::1]'].includes(
      parsed.hostname,
    );
    const secureProtocol =
      parsed.protocol === 'https:' || (loopback && parsed.protocol === 'http:');
    return (
      secureProtocol &&
      !parsed.username &&
      !parsed.password &&
      !parsed.search &&
      !parsed.hash &&
      (parsed.pathname === '' || parsed.pathname === '/')
    );
  } catch {
    return false;
  }
};

const isValidTopupGroupRatios = (value) => {
  try {
    const parsed = JSON.parse((value || '').trim());
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return false;
    }
    const ratios = Object.entries(parsed);
    return (
      ratios.length >= 1 &&
      ratios.length <= 100 &&
      ratios.every(
        ([group, ratio]) =>
          group.trim().length >= 1 &&
          group.length <= 64 &&
          typeof ratio === 'number' &&
          Number.isFinite(ratio) &&
          ratio > 0 &&
          ratio <= 1000,
      )
    );
  } catch {
    return false;
  }
};

export default function SettingsGeneralPayment(props) {
  const { t } = useTranslation();
  const sectionTitle = props.hideSectionTitle ? undefined : t('通用设置');
  const [loading, setLoading] = useState(false);
  const [inputs, setInputs] = useState({
    CustomCallbackAddress: '',
    TopupGroupRatio: '',
    PayMethods: '',
    AmountOptions: '',
    AmountDiscount: '',
  });
  const [originInputs, setOriginInputs] = useState({});
  const formApiRef = useRef(null);
  const submitInFlightRef = useRef(false);

  useEffect(() => {
    if (props.options && formApiRef.current) {
      const currentInputs = {
        CustomCallbackAddress: props.options.CustomCallbackAddress || '',
        TopupGroupRatio: props.options.TopupGroupRatio || '',
        PayMethods: props.options.PayMethods || '',
        AmountOptions: props.options.AmountOptions || '',
        AmountDiscount: props.options.AmountDiscount || '',
      };
      setInputs(currentInputs);
      setOriginInputs({ ...currentInputs });
      formApiRef.current.setValues(currentInputs);
    }
  }, [props.options]);

  const handleFormChange = (values) => {
    setInputs(values);
  };

  const submitGeneralSettings = async () => {
    if (submitInFlightRef.current) return;

    const callbackBase = removeTrailingSlash(inputs.CustomCallbackAddress);
    if (!isSecurePaymentCallbackOrigin(callbackBase)) {
      showError(
        t(
          'Enter a required HTTPS payment callback origin without a path, query, credentials, or fragment.',
        ),
      );
      return;
    }
    if (
      originInputs.TopupGroupRatio !== inputs.TopupGroupRatio &&
      !isValidTopupGroupRatios(inputs.TopupGroupRatio)
    ) {
      showError(t('充值分组倍率不是合法的 JSON 字符串'));
      return;
    }

    if (
      originInputs.PayMethods !== inputs.PayMethods &&
      !verifyJSON(inputs.PayMethods)
    ) {
      showError(t('充值方式设置不是合法的 JSON 字符串'));
      return;
    }

    if (
      originInputs.AmountOptions !== inputs.AmountOptions &&
      inputs.AmountOptions.trim() !== '' &&
      !verifyJSON(inputs.AmountOptions)
    ) {
      showError(t('自定义充值数量选项不是合法的 JSON 数组'));
      return;
    }

    if (
      originInputs.AmountDiscount !== inputs.AmountDiscount &&
      inputs.AmountDiscount.trim() !== '' &&
      !verifyJSON(inputs.AmountDiscount)
    ) {
      showError(t('充值金额折扣配置不是合法的 JSON 对象'));
      return;
    }

    submitInFlightRef.current = true;
    setLoading(true);
    try {
      const options = {};
      if (originInputs.CustomCallbackAddress !== inputs.CustomCallbackAddress) {
        options.CustomCallbackAddress = callbackBase;
      }
      if (originInputs.TopupGroupRatio !== inputs.TopupGroupRatio) {
        options.TopupGroupRatio = inputs.TopupGroupRatio;
      }
      if (originInputs.PayMethods !== inputs.PayMethods) {
        options.PayMethods = inputs.PayMethods;
      }
      if (originInputs.AmountOptions !== inputs.AmountOptions) {
        options['payment_setting.amount_options'] =
          inputs.AmountOptions.trim() || '[]';
      }
      if (originInputs.AmountDiscount !== inputs.AmountDiscount) {
        options['payment_setting.amount_discount'] =
          inputs.AmountDiscount.trim() || '{}';
      }

      if (Object.keys(options).length === 0) {
        showSuccess(t('更新成功'));
        return;
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
        if (response.data.success) {
          showSuccess(t('更新成功'));
          setOriginInputs({ ...inputs });
          await props.refresh?.(response.data?.data?.version);
        } else {
          showError(response.data.message || t('更新失败'));
        }
        return response;
      });
    } catch (error) {
      showError(error?.response?.data?.message || t('更新失败'));
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
          <Row gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}>
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.Input
                field='CustomCallbackAddress'
                label={t('Secure payment callback base')}
                placeholder={t('例如：https://yourdomain.com')}
                rules={[
                  {
                    required: true,
                    message: t('支付回调安全基址为必填项'),
                  },
                ]}
                extraText={t(
                  'Epay、Stripe 与 XORPay 的新支付只使用此 HTTPS 站点源生成回调与返回地址。请勿填写路径、查询参数、凭据或片段。',
                )}
              />
            </Col>
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.TextArea
                field='TopupGroupRatio'
                label={t('充值分组倍率')}
                placeholder={t('为一个 JSON 文本，键为组名称，值为倍率')}
                autosize
              />
            </Col>
          </Row>
          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.TextArea
                field='PayMethods'
                label={t('充值方式设置')}
                placeholder={t('为一个 JSON 文本')}
                autosize
              />
            </Col>
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.TextArea
                field='AmountOptions'
                label={t('自定义充值数量选项')}
                placeholder={t(
                  '为一个 JSON 数组，例如：[10, 20, 50, 100, 200, 500]',
                )}
                autosize
                extraText={t(
                  '设置用户可选择的充值数量选项，例如：[10, 20, 50, 100, 200, 500]',
                )}
              />
            </Col>
          </Row>
          <Row style={{ marginTop: 16 }}>
            <Col span={24}>
              <Form.TextArea
                field='AmountDiscount'
                label={t('充值金额折扣配置')}
                placeholder={t(
                  '为一个 JSON 对象，例如：{"100": 0.95, "200": 0.9, "500": 0.85}',
                )}
                autosize
                extraText={t(
                  '设置不同充值金额对应的折扣，键为充值金额，值为折扣率，例如：{"100": 0.95, "200": 0.9, "500": 0.85}',
                )}
              />
            </Col>
          </Row>
          <Button
            onClick={submitGeneralSettings}
            loading={loading}
            style={{ marginTop: 16 }}
          >
            {t('保存通用设置')}
          </Button>
        </Form.Section>
      </Form>
    </Spin>
  );
}
