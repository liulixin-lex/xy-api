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
import {
  Banner,
  Button,
  Form,
  Row,
  Col,
  Typography,
  Spin,
  Table,
  Modal,
  Input,
  Space,
} from '@douyinfe/semi-ui';
import {
  API,
  removeTrailingSlash,
  showError,
  showSuccess,
  showWarning,
} from '../../../helpers';
import { useTranslation } from 'react-i18next';
import { BookOpen, TriangleAlert } from 'lucide-react';
import { getSafePaymentIconUrl } from '../../../components/topup/payment-utils';
import {
  createPaymentAdminError,
  getPaymentAdminErrorMessage,
} from '../../../helpers/payment-admin-errors';
import RetainedCredentialEmergencyControl from './RetainedCredentialEmergencyControl';

const { Text } = Typography;
const toBoolean = (value) => value === true || value === 'true';

export default function SettingsPaymentGatewayWaffo(props) {
  const { t } = useTranslation();
  const sectionTitle = props.hideSectionTitle ? undefined : t('Waffo 设置');
  const [loading, setLoading] = useState(false);
  const [inputs, setInputs] = useState({
    WaffoEnabled: false,
    WaffoApiKey: '',
    WaffoPrivateKey: '',
    WaffoPublicCert: '',
    WaffoSandboxPublicCert: '',
    WaffoSandboxApiKey: '',
    WaffoSandboxPrivateKey: '',
    WaffoSandbox: false,
    WaffoMerchantId: '',
    WaffoCurrency: 'USD',
    WaffoUnitPrice: 1.0,
    WaffoMinTopUp: 1,
    WaffoNotifyUrl: '',
    WaffoReturnUrl: '',
    WaffoWebRedirectHosts: '',
    WaffoAppRedirectSchemes: '',
  });
  const formApiRef = useRef(null);
  const iconFileInputRef = useRef(null);

  const handleIconFileChange = (e) => {
    const file = e.target.files[0];
    if (!file) return;
    if (
      !['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(
        file.type,
      )
    ) {
      showError(t('仅支持 PNG、JPG、GIF 或 WebP 图片'));
      e.target.value = '';
      return;
    }
    const MAX_ICON_SIZE = 100 * 1024; // 100 KB
    if (file.size > MAX_ICON_SIZE) {
      showError(t('图标文件不能超过 100KB，请压缩后重新上传'));
      e.target.value = '';
      return;
    }
    const reader = new FileReader();
    reader.onload = (event) => {
      setPayMethodForm((prev) => ({ ...prev, icon: event.target.result }));
    };
    reader.readAsDataURL(file);
    e.target.value = '';
  };

  // 支付方式列表
  const [waffoPayMethods, setWaffoPayMethods] = useState([]);
  // 支付方式弹窗
  const [payMethodModalVisible, setPayMethodModalVisible] = useState(false);
  // 当前编辑的索引，-1 表示新增
  const [editingPayMethodIndex, setEditingPayMethodIndex] = useState(-1);
  // 弹窗内表单字段的临时状态
  const [payMethodForm, setPayMethodForm] = useState({
    name: '',
    icon: '',
    payMethodType: '',
    payMethodName: '',
  });

  useEffect(() => {
    if (props.options && formApiRef.current) {
      const currentInputs = {
        WaffoEnabled: toBoolean(props.options.WaffoEnabled),
        WaffoApiKey: props.options.WaffoApiKey || '',
        WaffoPrivateKey: props.options.WaffoPrivateKey || '',
        WaffoPublicCert: props.options.WaffoPublicCert || '',
        WaffoSandboxPublicCert: props.options.WaffoSandboxPublicCert || '',
        WaffoSandboxApiKey: props.options.WaffoSandboxApiKey || '',
        WaffoSandboxPrivateKey: props.options.WaffoSandboxPrivateKey || '',
        WaffoSandbox: toBoolean(props.options.WaffoSandbox),
        WaffoMerchantId: props.options.WaffoMerchantId || '',
        WaffoCurrency: props.options.WaffoCurrency || 'USD',
        WaffoUnitPrice: parseFloat(props.options.WaffoUnitPrice) || 1.0,
        WaffoMinTopUp: parseInt(props.options.WaffoMinTopUp) || 1,
        WaffoNotifyUrl: props.options.WaffoNotifyUrl || '',
        WaffoReturnUrl: props.options.WaffoReturnUrl || '',
        WaffoWebRedirectHosts: props.options.WaffoWebRedirectHosts || '',
        WaffoAppRedirectSchemes: props.options.WaffoAppRedirectSchemes || '',
      };
      setInputs(currentInputs);
      formApiRef.current.setValues(currentInputs);

      // 解析支付方式列表
      try {
        const rawPayMethods = props.options.WaffoPayMethods;
        if (rawPayMethods) {
          const parsed = JSON.parse(rawPayMethods);
          if (Array.isArray(parsed)) {
            setWaffoPayMethods(parsed);
          }
        }
      } catch {
        setWaffoPayMethods([]);
      }
    }
  }, [props.options]);

  const handleFormChange = (values) => {
    setInputs(values);
  };

  const submitWaffoSetting = async () => {
    const minTopUp = Number(inputs.WaffoMinTopUp);
    const unitPrice = Number(inputs.WaffoUnitPrice);
    if (!Number.isInteger(minTopUp) || minTopUp < 1 || minTopUp > 10000) {
      showError(t('最低充值数量必须是 1 到 10000 之间的整数'));
      return;
    }
    if (!Number.isFinite(unitPrice) || unitPrice <= 0) {
      showError(t('充值价格必须是大于 0 的数字'));
      return;
    }

    setLoading(true);
    try {
      const options = {
        WaffoEnabled: !!inputs.WaffoEnabled,
        WaffoPublicCert: (inputs.WaffoPublicCert || '').trim(),
        WaffoSandboxPublicCert: (inputs.WaffoSandboxPublicCert || '').trim(),
        WaffoSandbox: !!inputs.WaffoSandbox,
        WaffoMerchantId: (inputs.WaffoMerchantId || '').trim(),
        WaffoCurrency: (inputs.WaffoCurrency || 'USD').trim().toUpperCase(),
        WaffoUnitPrice: unitPrice,
        WaffoMinTopUp: minTopUp,
        WaffoNotifyUrl: (inputs.WaffoNotifyUrl || '').trim(),
        WaffoReturnUrl: (inputs.WaffoReturnUrl || '').trim(),
        WaffoWebRedirectHosts: (inputs.WaffoWebRedirectHosts || '').trim(),
        WaffoAppRedirectSchemes: (inputs.WaffoAppRedirectSchemes || '').trim(),
        WaffoPayMethods: JSON.stringify(waffoPayMethods),
      };

      if ((inputs.WaffoApiKey || '').trim()) {
        options.WaffoApiKey = inputs.WaffoApiKey.trim();
      }

      if ((inputs.WaffoPrivateKey || '').trim()) {
        options.WaffoPrivateKey = inputs.WaffoPrivateKey.trim();
      }

      if ((inputs.WaffoSandboxApiKey || '').trim()) {
        options.WaffoSandboxApiKey = inputs.WaffoSandboxApiKey.trim();
      }

      if ((inputs.WaffoSandboxPrivateKey || '').trim()) {
        options.WaffoSandboxPrivateKey = inputs.WaffoSandboxPrivateKey.trim();
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
        if (response.data?.success) {
          const nextInputs = {
            ...inputs,
            WaffoApiKey: '',
            WaffoPrivateKey: '',
            WaffoSandboxApiKey: '',
            WaffoSandboxPrivateKey: '',
          };
          setInputs(nextInputs);
          formApiRef.current?.setValues(nextInputs);
          const refreshed = await props.refresh?.(response.data?.data?.version);
          if (refreshed === false) {
            showWarning(t('设置已保存，但最新状态刷新失败'));
          } else {
            showSuccess(t('更新成功'));
          }
        } else {
          throw createPaymentAdminError(response.data, t('更新失败'));
        }
        return response;
      });
    } catch (error) {
      showError(getPaymentAdminErrorMessage(error, t, t('更新失败')));
    } finally {
      setLoading(false);
    }
  };

  // 打开新增弹窗
  const openAddPayMethodModal = () => {
    setEditingPayMethodIndex(-1);
    setPayMethodForm({
      name: '',
      icon: '',
      payMethodType: '',
      payMethodName: '',
    });
    setPayMethodModalVisible(true);
  };

  // 打开编辑弹窗
  const openEditPayMethodModal = (record, index) => {
    setEditingPayMethodIndex(index);
    setPayMethodForm({
      name: record.name || '',
      icon: record.icon || '',
      payMethodType: record.payMethodType || '',
      payMethodName: record.payMethodName || '',
    });
    setPayMethodModalVisible(true);
  };

  // 确认保存弹窗（新增或编辑）
  const handlePayMethodModalOk = () => {
    if (!payMethodForm.name || payMethodForm.name.trim() === '') {
      showError(t('支付方式名称不能为空'));
      return;
    }
    const newMethod = {
      name: payMethodForm.name.trim(),
      icon: payMethodForm.icon.trim(),
      payMethodType: payMethodForm.payMethodType.trim(),
      payMethodName: payMethodForm.payMethodName.trim(),
    };
    if (editingPayMethodIndex === -1) {
      // 新增
      setWaffoPayMethods([...waffoPayMethods, newMethod]);
    } else {
      // 编辑
      const updated = [...waffoPayMethods];
      updated[editingPayMethodIndex] = newMethod;
      setWaffoPayMethods(updated);
    }
    setPayMethodModalVisible(false);
  };

  // 删除支付方式
  const handleDeletePayMethod = (index) => {
    const updated = waffoPayMethods.filter((_, i) => i !== index);
    setWaffoPayMethods(updated);
  };

  // 支付方式表格列定义
  const payMethodColumns = [
    {
      title: t('显示名称'),
      dataIndex: 'name',
    },
    {
      title: t('图标'),
      dataIndex: 'icon',
      render: (text) => {
        const safeIconUrl = getSafePaymentIconUrl(text);
        return safeIconUrl ? (
          <img
            src={safeIconUrl}
            alt=''
            aria-hidden='true'
            loading='lazy'
            decoding='async'
            referrerPolicy='no-referrer'
            style={{ width: 24, height: 24, objectFit: 'contain' }}
          />
        ) : (
          <Text type='tertiary'>-</Text>
        );
      },
    },
    {
      title: t('支付方式类型'),
      dataIndex: 'payMethodType',
      render: (text) => text || <Text type='tertiary'>-</Text>,
    },
    {
      title: t('支付方式名称'),
      dataIndex: 'payMethodName',
      render: (text) => text || <Text type='tertiary'>-</Text>,
    },
    {
      title: t('操作'),
      key: 'action',
      render: (_, record, index) => (
        <Space>
          <Button
            size='small'
            onClick={() => openEditPayMethodModal(record, index)}
          >
            {t('编辑')}
          </Button>
          <Button
            size='small'
            type='danger'
            onClick={() => handleDeletePayMethod(index)}
          >
            {t('删除')}
          </Button>
        </Space>
      ),
    },
  ];
  const callbackBaseAddress = removeTrailingSlash(
    props.options.CustomCallbackAddress || props.options.ServerAddress || '',
  );
  const displayedWebhookUrl = (inputs.WaffoNotifyUrl || '').trim()
    ? removeTrailingSlash(inputs.WaffoNotifyUrl)
    : callbackBaseAddress
      ? `${callbackBaseAddress}/api/waffo/webhook`
      : `${t('网站地址')}/api/waffo/webhook`;

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
            description={
              <>
                {t('Waffo 密钥、商户和支付方式等设置请')}
                <a href='https://waffo.com' target='_blank' rel='noreferrer'>
                  {t('点击此处')}
                </a>
                {t('进行配置，切换沙盒模式时请同步填写对应环境的密钥。')}
                <br />
                {t('回调地址')}：{displayedWebhookUrl}
              </>
            }
            style={{ marginBottom: 12 }}
          />
          <Banner
            type='warning'
            icon={<TriangleAlert size={16} />}
            description={t('请确认商户和所选环境密钥一致。')}
            style={{ marginBottom: 16 }}
          />

          <Row gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.Switch
                field='WaffoEnabled'
                label={t('启用 Waffo')}
                size='default'
                checkedText='｜'
                uncheckedText='〇'
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.Switch
                field='WaffoSandbox'
                label={t('沙盒模式')}
                size='default'
                checkedText='｜'
                uncheckedText='〇'
                extraText={t('用于切换当前下单和回调校验所使用的环境')}
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.Input
                field='WaffoMerchantId'
                label={t('商户 ID')}
                placeholder={t('例如：MER_xxx')}
                extraText={t('当前环境共用同一商户 ID')}
              />
            </Col>
          </Row>

          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.Input
                field='WaffoApiKey'
                label={t('API 密钥（生产环境）')}
                placeholder={t(
                  '填写后覆盖当前生产环境 API 密钥，留空表示保持当前不变',
                )}
                extraText={t('保存后不会回显，请填写生产环境对应的 API 密钥')}
                type='password'
                autoComplete='new-password'
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.TextArea
                field='WaffoPrivateKey'
                label={t('API 私钥（生产环境）')}
                placeholder={t(
                  '填写后覆盖当前生产环境私钥，留空表示保持当前不变',
                )}
                extraText={t('保存后不会回显，请填写生产环境对应的 API 私钥')}
                type='password'
                autoComplete='new-password'
                autosize={{ minRows: 3, maxRows: 6 }}
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.TextArea
                field='WaffoPublicCert'
                label={t('Waffo 公钥（生产环境）')}
                placeholder={t(
                  '填写生产环境 Waffo 公钥，Base64 或 PEM 内容均可',
                )}
                extraText={t('用于校验生产环境的 Waffo 回调签名')}
                type='password'
                autoComplete='new-password'
                autosize={{ minRows: 3, maxRows: 6 }}
              />
            </Col>
          </Row>

          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.Input
                field='WaffoSandboxApiKey'
                label={t('API 密钥（测试环境）')}
                placeholder={t(
                  '填写后覆盖当前测试环境 API 密钥，留空表示保持当前不变',
                )}
                extraText={t('保存后不会回显，请填写测试环境对应的 API 密钥')}
                type='password'
                autoComplete='new-password'
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.TextArea
                field='WaffoSandboxPrivateKey'
                label={t('API 私钥（测试环境）')}
                placeholder={t(
                  '填写后覆盖当前测试环境私钥，留空表示保持当前不变',
                )}
                extraText={t('保存后不会回显，请填写测试环境对应的 API 私钥')}
                type='password'
                autoComplete='new-password'
                autosize={{ minRows: 3, maxRows: 6 }}
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.TextArea
                field='WaffoSandboxPublicCert'
                label={t('Waffo 公钥（测试环境）')}
                placeholder={t(
                  '填写测试环境 Waffo 公钥，Base64 或 PEM 内容均可',
                )}
                extraText={t('用于校验测试环境的 Waffo 回调签名')}
                type='password'
                autoComplete='new-password'
                autosize={{ minRows: 3, maxRows: 6 }}
              />
            </Col>
          </Row>

          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.Input
                field='WaffoCurrency'
                label={t('货币')}
                placeholder='USD'
                extraText={t('Waffo 当前使用 USD 结算')}
                disabled
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.InputNumber
                field='WaffoUnitPrice'
                precision={2}
                label={t('充值价格（x元/美金）')}
                placeholder={t('例如：7，就是7元/美金')}
                extraText={t('按 1 美元对应的站内价格填写')}
                min={0}
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.InputNumber
                field='WaffoMinTopUp'
                label={t('最低充值美元数量')}
                placeholder={t('例如：2，就是最低充值2$')}
                extraText={t('用户单次最少可充值的美元数量')}
                min={1}
              />
            </Col>
          </Row>

          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.Input
                field='WaffoNotifyUrl'
                label={t('回调地址')}
                placeholder={t('例如：https://example.com/api/waffo/webhook')}
                extraText={t('留空则自动使用当前站点的默认回调地址')}
              />
            </Col>
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.Input
                field='WaffoReturnUrl'
                label={t('支付返回地址')}
                placeholder={t('例如：https://example.com/console/topup')}
                extraText={t('留空则自动使用当前站点的默认充值页地址')}
              />
            </Col>
          </Row>

          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.TextArea
                field='WaffoWebRedirectHosts'
                label={t('Additional trusted web checkout hosts')}
                placeholder='checkout.partner.example'
                extraText={t(
                  'One exact HTTPS hostname per line. cashier.waffo.com is always trusted.',
                )}
                autosize={{ minRows: 3, maxRows: 6 }}
              />
            </Col>
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.TextArea
                field='WaffoAppRedirectSchemes'
                label={t('Allowed App deep-link schemes')}
                placeholder='weixin'
                extraText={t(
                  'Leave empty to disable App deep links. Add only schemes confirmed by your Waffo payment method, one per line.',
                )}
                autosize={{ minRows: 3, maxRows: 6 }}
              />
            </Col>
          </Row>
        </Form.Section>

        <Form.Section text={t('支付方式设置')}>
          <Text type='secondary'>
            {t(
              '这里配置 Waffo 下展示给用户的 Card、Apple Pay、Google Pay 等子支付方式。',
            )}
          </Text>
          <div style={{ marginTop: 12, marginBottom: 12 }}>
            <Button onClick={openAddPayMethodModal}>{t('新增支付方式')}</Button>
          </div>
          <Table
            columns={payMethodColumns}
            dataSource={waffoPayMethods}
            rowKey={(record, index) => index}
            pagination={false}
            size='small'
            empty={
              <Text type='tertiary'>{t('暂无支付方式，点击上方按钮新增')}</Text>
            }
          />
          <Button onClick={submitWaffoSetting} style={{ marginTop: 16 }}>
            {t('更新 Waffo 设置')}
          </Button>

          <RetainedCredentialEmergencyControl
            provider='waffo'
            disabled={loading}
            withPaymentVerification={props.withPaymentVerification}
            onCompleted={async (result) => {
              const certificateField = inputs.WaffoSandbox
                ? 'WaffoSandboxPublicCert'
                : 'WaffoPublicCert';
              const nextInputs = {
                ...inputs,
                WaffoEnabled: false,
                WaffoApiKey: '',
                WaffoPrivateKey: '',
                WaffoSandboxApiKey: '',
                WaffoSandboxPrivateKey: '',
                [certificateField]: '',
              };
              setInputs(nextInputs);
              formApiRef.current?.setValues(nextInputs);
              return (await props.refresh?.(result.data?.version)) !== false;
            }}
            onStale={() => props.refresh?.()}
          />
        </Form.Section>
      </Form>

      {/* 新增/编辑支付方式弹窗 */}
      <Modal
        title={
          editingPayMethodIndex === -1 ? t('新增支付方式') : t('编辑支付方式')
        }
        visible={payMethodModalVisible}
        onOk={handlePayMethodModalOk}
        onCancel={() => setPayMethodModalVisible(false)}
        okText={t('确定')}
        cancelText={t('取消')}
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <div>
            <div style={{ marginBottom: 4 }}>
              <Text strong>{t('显示名称')}</Text>
              <span
                style={{ color: 'var(--semi-color-danger)', marginLeft: 4 }}
              >
                *
              </span>
            </div>
            <Input
              value={payMethodForm.name}
              onChange={(val) =>
                setPayMethodForm({ ...payMethodForm, name: val })
              }
              placeholder={t('例如：Credit Card')}
            />
            <Text type='tertiary' size='small'>
              {t('用户在充值页面看到的支付方式名称，例如：Credit Card')}
            </Text>
          </div>
          <div>
            <div style={{ marginBottom: 4 }}>
              <Text strong>{t('图标')}</Text>
            </div>
            <Space align='center'>
              {payMethodForm.icon &&
                (() => {
                  const safeIconUrl = getSafePaymentIconUrl(payMethodForm.icon);
                  return safeIconUrl ? (
                    <img
                      src={safeIconUrl}
                      alt={t('支付方式图标预览')}
                      referrerPolicy='no-referrer'
                      style={{
                        width: 32,
                        height: 32,
                        objectFit: 'contain',
                        border: '1px solid var(--semi-color-border)',
                        borderRadius: 4,
                      }}
                    />
                  ) : null;
                })()}
              <input
                type='file'
                accept='image/png,image/jpeg,image/gif,image/webp'
                ref={iconFileInputRef}
                style={{ display: 'none' }}
                onChange={handleIconFileChange}
              />
              <Button
                size='small'
                onClick={() => iconFileInputRef.current?.click()}
              >
                {payMethodForm.icon ? t('重新上传') : t('上传图片')}
              </Button>
              {payMethodForm.icon && (
                <Button
                  size='small'
                  type='danger'
                  onClick={() =>
                    setPayMethodForm((prev) => ({ ...prev, icon: '' }))
                  }
                >
                  {t('清除')}
                </Button>
              )}
            </Space>
            <div>
              <Text type='tertiary' size='small'>
                {t('上传 PNG/JPG/GIF/WebP 图片，建议尺寸不超过 128×128px')}
              </Text>
            </div>
          </div>
          <div>
            <div style={{ marginBottom: 4 }}>
              <Text strong>{t('支付方式类型')}</Text>
            </div>
            <Input
              value={payMethodForm.payMethodType}
              onChange={(val) =>
                setPayMethodForm({ ...payMethodForm, payMethodType: val })
              }
              placeholder='CREDITCARD,DEBITCARD'
              maxLength={64}
            />
            <Text type='tertiary' size='small'>
              {t(
                'Waffo API 参数，可空，例如：CREDITCARD,DEBITCARD（最多64位）',
              )}
            </Text>
          </div>
          <div>
            <div style={{ marginBottom: 4 }}>
              <Text strong>{t('支付方式名称')}</Text>
            </div>
            <Input
              value={payMethodForm.payMethodName}
              onChange={(val) =>
                setPayMethodForm({ ...payMethodForm, payMethodName: val })
              }
              placeholder={t('可空')}
              maxLength={64}
            />
            <Text type='tertiary' size='small'>
              {t('Waffo API 参数，可空（最多64位）')}
            </Text>
          </div>
        </div>
      </Modal>
    </Spin>
  );
}
