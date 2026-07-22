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

import React, { useEffect, useMemo, useState } from 'react';
import {
  Banner,
  Button,
  Card,
  Skeleton,
  Tag,
  Typography,
} from '@douyinfe/semi-ui';
import {
  AlertTriangle,
  ArrowLeft,
  CheckCircle2,
  Clock3,
  Copy,
  ExternalLink,
  Info,
  LoaderCircle,
  QrCode,
  RefreshCw,
  ShieldCheck,
  Smartphone,
} from 'lucide-react';
import { SiAlipay, SiWechat } from 'react-icons/si';
import { QRCodeSVG } from 'qrcode.react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router-dom';

import { copy, getLogo, getSystemName } from '../../helpers';
import { usePaymentOrderPolling } from '../../components/topup/use-payment-order';
import {
  detectPaymentBrowserEnvironment,
  formatPaymentDecimal,
  getPublicPaymentMethodLabel,
  getSafePaymentContinueUrl,
  getSafeWeChatAuthorizationUrl,
  getSafePaymentUrl,
  isSafeJSAPIParameters,
  isPaymentReturnCancelled,
  isSafeQrContent,
  navigateToPaymentUrl,
} from '../../components/topup/payment-utils';
import './payment.css';

const { Text, Title } = Typography;

const STATUS_META = {
  preparing: { label: '支付准备中', color: 'blue' },
  awaiting_payment: { label: '等待支付', color: 'orange' },
  confirming: { label: '确认中', color: 'blue' },
  succeeded: { label: '支付成功', color: 'green' },
  expired: { label: '已过期', color: 'grey' },
  temporarily_unavailable: { label: '暂时不可用', color: 'red' },
};

function getChannelLabel(channel, t) {
  if (channel === 'qr' || channel === 'native') return t('扫码支付');
  if (channel === 'jsapi' || channel === 'wechat_browser')
    return t('微信内支付');
  if (channel === 'redirect' || channel === 'checkout')
    return t('Secure checkout');
  return '';
}

function formatRemainingTime(totalSeconds) {
  const safeSeconds = Math.max(0, totalSeconds);
  return {
    minutes: String(Math.floor(safeSeconds / 60)).padStart(2, '0'),
    seconds: String(safeSeconds % 60).padStart(2, '0'),
  };
}

function invokeWeChatJSAPI(parameters, callback) {
  let finished = false;
  let timeoutId = 0;
  const cleanup = () => {
    document.removeEventListener('WeixinJSBridgeReady', onBridgeReady);
    document.removeEventListener('onWeixinJSBridgeReady', onBridgeReady);
    if (timeoutId) window.clearTimeout(timeoutId);
  };
  const invoke = () => {
    if (!window.WeixinJSBridge || finished) return false;
    finished = true;
    cleanup();
    window.WeixinJSBridge.invoke('getBrandWCPayRequest', parameters, callback);
    return true;
  };
  const onBridgeReady = () => {
    invoke();
  };
  if (invoke()) return;
  document.addEventListener('WeixinJSBridgeReady', onBridgeReady, {
    once: true,
  });
  document.addEventListener('onWeixinJSBridgeReady', onBridgeReady, {
    once: true,
  });
  timeoutId = window.setTimeout(() => {
    if (finished) return;
    finished = true;
    cleanup();
    callback({ err_msg: 'bridge_unavailable' });
  }, 5000);
}

function StatePanel({ icon, iconClassName, title, description, actions }) {
  return (
    <div className='payment-checkout-state'>
      <span className={`payment-checkout-state-icon ${iconClassName || ''}`}>
        {icon}
      </span>
      <Title heading={4}>{title}</Title>
      <Text type='tertiary' className='payment-checkout-state-description'>
        {description}
      </Text>
      {actions ? (
        <div className='payment-checkout-actions'>{actions}</div>
      ) : null}
    </div>
  );
}

function CheckoutPanel({
  order,
  status,
  environment,
  refreshing,
  onRefresh,
  onBack,
}) {
  const { t } = useTranslation();
  const [copyState, setCopyState] = useState('idle');
  const [jsapiState, setJSAPIState] = useState('idle');
  const checkout = order.checkout;
  const isAlipay = order.public_method === 'alipay';
  const isWechatPay = order.public_method === 'wechat_pay';

  const handleCopyPageLink = async () => {
    const copied = await copy(window.location.href);
    setCopyState(copied ? 'copied' : 'failed');
    if (copied) {
      window.setTimeout(() => setCopyState('idle'), 2400);
    }
  };

  if (status === 'succeeded') {
    return (
      <StatePanel
        icon={<CheckCircle2 size={30} aria-hidden='true' />}
        iconClassName='is-success'
        title={t('支付完成')}
        description={
          order.plan_id ? t('您的定期权益已生效。') : t('您的余额已更新。')
        }
        actions={<Button onClick={onBack}>{t('返回钱包')}</Button>}
      />
    );
  }

  if (status === 'confirming') {
    return (
      <StatePanel
        icon={
          <LoaderCircle
            size={30}
            className='payment-checkout-spin'
            aria-hidden='true'
          />
        }
        iconClassName='is-info'
        title={t('确认支付中')}
        description={t(
          '支付已提交，正在确认结果。您可以保持页面打开，也可以稍后返回查看。',
        )}
      />
    );
  }

  if (status === 'expired') {
    return (
      <StatePanel
        icon={<Clock3 size={30} aria-hidden='true' />}
        iconClassName='is-warning'
        title={t('支付已过期')}
        description={t('此订单已无法继续支付，请返回钱包重新创建。')}
        actions={<Button onClick={onBack}>{t('重新发起支付')}</Button>}
      />
    );
  }

  if (status === 'temporarily_unavailable') {
    return (
      <StatePanel
        icon={<AlertTriangle size={30} aria-hidden='true' />}
        iconClassName='is-danger'
        title={t('支付暂时不可用')}
        description={t('当前订单暂时无法继续，请刷新状态或重新发起支付。')}
        actions={
          <>
            <Button
              theme='outline'
              icon={<RefreshCw size={16} />}
              onClick={onRefresh}
              loading={refreshing}
              disabled={refreshing}
            >
              {t('刷新状态')}
            </Button>
            <Button onClick={onBack}>{t('返回钱包')}</Button>
          </>
        }
      />
    );
  }

  if (!checkout || checkout.flow === 'pending') {
    return (
      <StatePanel
        icon={
          <LoaderCircle
            size={30}
            className='payment-checkout-spin'
            aria-hidden='true'
          />
        }
        iconClassName='is-info'
        title={t('准备您的支付')}
        description={t(
          '通常只需几秒。您可以安全离开，稍后凭同一订单编号返回。',
        )}
      />
    );
  }

  if (checkout.flow === 'wechat_authorize') {
    const authorizationURL = getSafeWeChatAuthorizationUrl(
      checkout.continue_url,
      order.trade_no,
    );
    if (environment !== 'wechat') {
      return (
        <StatePanel
          icon={<Smartphone size={30} aria-hidden='true' />}
          iconClassName='is-success'
          title={t('在微信中打开')}
          description={t('此支付方式需要在微信内完成，请使用微信打开本页面。')}
          actions={
            environment === 'mobile' ? (
              <div className='payment-checkout-copy-area' aria-live='polite'>
                <Button
                  theme='outline'
                  icon={<Copy size={16} />}
                  onClick={handleCopyPageLink}
                >
                  {copyState === 'copied'
                    ? t('页面链接已复制')
                    : t('复制页面链接')}
                </Button>
                {copyState === 'failed' ? (
                  <Text type='danger'>
                    {t('Unable to copy. Copy the browser address manually.')}
                  </Text>
                ) : null}
              </div>
            ) : null
          }
        />
      );
    }
    return (
      <StatePanel
        icon={<Smartphone size={30} aria-hidden='true' />}
        iconClassName='is-success'
        title={t('继续微信支付')}
        description={t('请确认微信账户，以安全准备本次支付。')}
        actions={
          authorizationURL ? (
            <Button
              icon={<Smartphone size={16} />}
              onClick={() => window.location.assign(authorizationURL.href)}
            >
              {t('在微信中继续')}
            </Button>
          ) : (
            <Banner
              type='danger'
              closeIcon={null}
              title={t('支付链接暂不可用')}
              description={t('请刷新订单状态后重试。')}
            />
          )
        }
      />
    );
  }

  if (checkout.flow === 'jsapi') {
    const parameters = checkout.jsapi;
    const safeParameters = isSafeJSAPIParameters(parameters);
    const invokePayment = () => {
      if (!safeParameters) {
        setJSAPIState('unavailable');
        return;
      }
      setJSAPIState('opening');
      invokeWeChatJSAPI(
        {
          appId: parameters.app_id,
          timeStamp: parameters.timestamp,
          nonceStr: parameters.nonce_str,
          package: parameters.package,
          signType: parameters.sign_type,
          paySign: parameters.pay_sign,
        },
        (result) => {
          const message = result?.err_msg || '';
          if (message === 'get_brand_wcpay_request:ok') {
            setJSAPIState('submitted');
            onRefresh();
          } else if (message === 'get_brand_wcpay_request:cancel') {
            setJSAPIState('cancelled');
          } else {
            setJSAPIState('unavailable');
          }
        },
      );
    };

    if (environment !== 'wechat') {
      return (
        <StatePanel
          icon={<Smartphone size={30} aria-hidden='true' />}
          iconClassName='is-success'
          title={t('在微信中打开')}
          description={t('支付已准备完成，请在微信中重新打开本订单继续。')}
        />
      );
    }
    if (!safeParameters) {
      return (
        <StatePanel
          icon={<AlertTriangle size={30} aria-hidden='true' />}
          iconClassName='is-danger'
          title={t('支付暂时不可用')}
          description={t('请刷新订单状态后重试。')}
          actions={
            <Button
              theme='outline'
              icon={<RefreshCw size={16} />}
              onClick={onRefresh}
              loading={refreshing}
              disabled={refreshing}
            >
              {t('刷新状态')}
            </Button>
          }
        />
      );
    }
    return (
      <StatePanel
        icon={<Smartphone size={30} aria-hidden='true' />}
        iconClassName='is-success'
        title={t('Pay with WeChat')}
        description={
          jsapiState === 'submitted'
            ? t('支付已提交，正在等待本站安全确认。')
            : t('微信将打开支付面板，最终成功结果仍以本站确认为准。')
        }
        actions={
          <div className='payment-checkout-actions payment-checkout-actions-mobile'>
            <Button
              icon={<Smartphone size={16} />}
              onClick={invokePayment}
              loading={jsapiState === 'opening'}
              disabled={jsapiState === 'opening' || jsapiState === 'submitted'}
              aria-busy={jsapiState === 'opening'}
            >
              {jsapiState === 'opening'
                ? t('Opening WeChat Pay')
                : t('立即支付')}
            </Button>
            {jsapiState === 'cancelled' ? (
              <Text type='tertiary'>{t('支付已取消，您可以重新尝试。')}</Text>
            ) : null}
            {jsapiState === 'unavailable' ? (
              <div className='payment-checkout-copy-area'>
                <Text type='danger'>
                  {t('未能打开微信支付，请刷新页面后重试。')}
                </Text>
                <Button
                  theme='outline'
                  icon={<RefreshCw size={16} />}
                  onClick={onRefresh}
                  loading={refreshing}
                  disabled={refreshing}
                >
                  {t('刷新状态')}
                </Button>
              </div>
            ) : null}
          </div>
        }
      />
    );
  }

  if (checkout.flow === 'hosted_redirect' || checkout.flow === 'form_post') {
    const continueUrl = getSafePaymentContinueUrl(
      checkout.continue_url,
      order.trade_no,
    );
    return (
      <StatePanel
        icon={<ExternalLink size={30} aria-hidden='true' />}
        iconClassName='is-info'
        title={t('继续支付')}
        description={t(
          '请前往安全支付页面完成操作，最终结果仍以本站确认为准。',
        )}
        actions={
          continueUrl ? (
            <Button
              icon={<ExternalLink size={16} />}
              onClick={() => window.location.assign(continueUrl.href)}
            >
              {t('前往支付')}
            </Button>
          ) : (
            <Banner
              type='danger'
              closeIcon={null}
              title={t('支付链接暂不可用')}
              description={t('请刷新订单状态后重试。')}
            />
          )
        }
      />
    );
  }

  const qrContent = String(checkout.qr_content || '').trim();
  const strictQrIsSafe = isSafeQrContent(qrContent);
  const genericQrIsSafe =
    !isAlipay && !isWechatPay && !!getSafePaymentUrl(qrContent);
  const qrIsSafe = strictQrIsSafe || genericQrIsSafe;
  const alipayUrl = isAlipay ? getSafePaymentUrl(qrContent) : null;

  if (!qrIsSafe || (isWechatPay && order.channel_alias === 'jsapi')) {
    return (
      <StatePanel
        icon={<AlertTriangle size={30} aria-hidden='true' />}
        iconClassName='is-danger'
        title={t('支付二维码暂不可用')}
        description={t('当前二维码无法安全使用，请刷新状态后重试。')}
        actions={
          <Button
            theme='outline'
            icon={<RefreshCw size={16} />}
            onClick={onRefresh}
            loading={refreshing}
            disabled={refreshing}
          >
            {t('刷新状态')}
          </Button>
        }
      />
    );
  }

  let instruction = t('请使用所选支付应用扫码。');
  if (isAlipay) {
    instruction =
      environment === 'desktop'
        ? t('请使用手机支付宝扫描二维码。')
        : t(
            '请打开支付宝完成支付；若未能拉起，可在系统浏览器中打开，或使用另一台设备扫码。',
          );
  } else if (isWechatPay) {
    if (environment === 'desktop') {
      instruction = t('请使用手机微信扫描二维码。');
    } else if (environment === 'wechat') {
      instruction = t('当前订单需要使用另一台设备展示二维码，再用微信扫一扫。');
    } else {
      instruction = t('请在微信中打开本页，或使用另一台设备扫描二维码。');
    }
  }

  return (
    <div className='payment-checkout-qr-panel'>
      <div
        className='payment-checkout-qr'
        role='img'
        aria-label={t('支付二维码')}
      >
        <QRCodeSVG value={qrContent} size={224} level='M' />
      </div>
      <Title heading={5}>{t('Scan to Pay')}</Title>
      <Text type='tertiary' className='payment-checkout-state-description'>
        {instruction}
      </Text>

      {isAlipay && environment !== 'desktop' && alipayUrl ? (
        <div className='payment-checkout-actions payment-checkout-actions-mobile'>
          <Button
            icon={<Smartphone size={16} />}
            onClick={() => navigateToPaymentUrl(alipayUrl.href)}
          >
            {t('打开支付宝')}
          </Button>
          <Button
            theme='outline'
            icon={<ExternalLink size={16} />}
            onClick={() =>
              window.open(alipayUrl.href, '_blank', 'noopener,noreferrer')
            }
          >
            {t('在浏览器中打开')}
          </Button>
        </div>
      ) : null}

      {isWechatPay && environment === 'mobile' ? (
        <div className='payment-checkout-copy-area' aria-live='polite'>
          <Button
            theme='outline'
            icon={<Copy size={16} />}
            onClick={handleCopyPageLink}
          >
            {copyState === 'copied' ? t('页面链接已复制') : t('复制页面链接')}
          </Button>
          {copyState === 'failed' ? (
            <Text type='danger'>
              {t('Unable to copy. Copy the browser address manually.')}
            </Text>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

const Payment = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { tradeNo = '' } = useParams();
  const normalizedTradeNo = tradeNo.trim();
  const validTradeNo =
    normalizedTradeNo.length > 0 && normalizedTradeNo.length <= 128;
  const [now, setNow] = useState(() => Date.now());
  const environment = useMemo(() => detectPaymentBrowserEnvironment(), []);
  const returnedCancelled = useMemo(
    () => isPaymentReturnCancelled(window.location.search),
    [],
  );
  const configuredSystemName = getSystemName();
  const systemName =
    configuredSystemName &&
    !['undefined', 'null'].includes(configuredSystemName.toLowerCase())
      ? configuredSystemName
      : 'New API';
  const configuredLogo = getLogo();
  const logo =
    configuredLogo &&
    !['undefined', 'null'].includes(configuredLogo.toLowerCase())
      ? configuredLogo
      : '/logo.png';
  const {
    order,
    error,
    polling,
    refreshing,
    trackPayment,
    refreshPayment,
    clearPayment,
  } = usePaymentOrderPolling({ t });

  useEffect(() => {
    if (validTradeNo) {
      trackPayment(
        { trade_no: normalizedTradeNo, flow: 'pending' },
        'preparing',
      );
    }
    return clearPayment;
  }, [clearPayment, normalizedTradeNo, trackPayment, validTradeNo]);

  const loadedOrder = order?.status_code ? order : null;
  const expiresAt = Number(
    loadedOrder?.checkout?.expires_at || loadedOrder?.expires_at || 0,
  );

  useEffect(() => {
    if (!expiresAt) return undefined;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [expiresAt]);

  const remainingSeconds = expiresAt
    ? Math.max(0, Math.floor(expiresAt - now / 1000))
    : 0;
  const remaining = formatRemainingTime(remainingSeconds);
  const rawStatus = loadedOrder?.status_code || 'preparing';
  const status =
    remainingSeconds === 0 &&
    expiresAt > 0 &&
    ['preparing', 'awaiting_payment'].includes(rawStatus)
      ? 'expired'
      : rawStatus;
  const statusMeta = STATUS_META[status] || STATUS_META.temporarily_unavailable;
  const methodLabel = loadedOrder
    ? getPublicPaymentMethodLabel(loadedOrder, t)
    : t('Payment method');
  const channelLabel = loadedOrder
    ? getChannelLabel(loadedOrder.channel_alias, t)
    : '';
  const amount = loadedOrder
    ? formatPaymentDecimal(
        loadedOrder.payment_amount,
        loadedOrder.currency,
        loadedOrder.public_method,
      )
    : error
      ? t('Not available')
      : t('准备中');
  const methodClassName =
    loadedOrder?.public_method === 'alipay'
      ? 'is-alipay'
      : loadedOrder?.public_method === 'wechat_pay'
        ? 'is-wechat'
        : 'is-generic';

  const goBack = () => navigate('/console/topup');

  return (
    <main className='payment-checkout-page'>
      <div className='payment-checkout-shell'>
        <div className='payment-checkout-topbar'>
          <Button
            theme='borderless'
            type='tertiary'
            icon={<ArrowLeft size={17} />}
            onClick={goBack}
          >
            {t('返回钱包')}
          </Button>
          <div className='payment-checkout-brand' aria-label={systemName}>
            {logo ? (
              <img src={logo} alt='' referrerPolicy='no-referrer' />
            ) : null}
            <span>{systemName}</span>
          </div>
        </div>

        {returnedCancelled &&
        ['preparing', 'awaiting_payment'].includes(status) ? (
          <Banner
            type='warning'
            closeIcon={null}
            description={t('支付已取消，您可以重新尝试。')}
            style={{ marginBottom: 16 }}
          />
        ) : null}

        <Card
          className={`payment-checkout-card ${methodClassName}`}
          bodyStyle={{ padding: 0 }}
        >
          <header className='payment-checkout-summary'>
            <div>
              <Text type='tertiary'>
                {loadedOrder?.plan_id ? t('权益购买') : t('安全支付')}
              </Text>
              <div className='payment-checkout-amount'>{amount}</div>
            </div>
            <div className='payment-checkout-summary-tags'>
              <span className='payment-checkout-method'>
                {loadedOrder?.public_method === 'alipay' ? (
                  <SiAlipay size={18} aria-hidden='true' />
                ) : loadedOrder?.public_method === 'wechat_pay' ? (
                  <SiWechat size={18} aria-hidden='true' />
                ) : (
                  <ShieldCheck size={18} aria-hidden='true' />
                )}
                {methodLabel}
              </span>
              {loadedOrder ? (
                <Tag color={statusMeta.color} size='large' shape='circle'>
                  {t(statusMeta.label)}
                </Tag>
              ) : null}
            </div>
          </header>

          <div className='payment-checkout-grid'>
            <section className='payment-checkout-main' aria-live='polite'>
              {!validTradeNo || (error && !loadedOrder) ? (
                <StatePanel
                  icon={<AlertTriangle size={30} aria-hidden='true' />}
                  iconClassName='is-danger'
                  title={t('无法加载支付订单')}
                  description={t('请检查网络后重试。')}
                  actions={
                    <Button
                      theme='outline'
                      icon={<RefreshCw size={16} />}
                      onClick={refreshPayment}
                      disabled={!validTradeNo || refreshing}
                      loading={refreshing}
                    >
                      {t('重试')}
                    </Button>
                  }
                />
              ) : !loadedOrder ? (
                <div className='payment-checkout-loading' role='status'>
                  <LoaderCircle
                    size={34}
                    className='payment-checkout-spin'
                    aria-hidden='true'
                  />
                  <Text>{t('Loading payment')}</Text>
                  <Skeleton.Title style={{ width: 220, height: 14 }} />
                </div>
              ) : (
                <CheckoutPanel
                  order={loadedOrder}
                  status={status}
                  environment={environment}
                  refreshing={refreshing}
                  onRefresh={refreshPayment}
                  onBack={goBack}
                />
              )}
            </section>

            <aside className='payment-checkout-details'>
              <Title heading={6}>{t('订单详情')}</Title>
              <dl>
                <div>
                  <dt>{t('订单编号')}</dt>
                  <dd className='payment-checkout-order-number'>
                    {loadedOrder?.trade_no || t('Not available')}
                  </dd>
                </div>
                <div>
                  <dt>{t('Payment Method')}</dt>
                  <dd>{loadedOrder ? methodLabel : t('Not available')}</dd>
                </div>
                {channelLabel ? (
                  <div>
                    <dt>{t('支付选项')}</dt>
                    <dd>{channelLabel}</dd>
                  </div>
                ) : null}
                <div>
                  <dt>{t('剩余时间')}</dt>
                  <dd className='payment-checkout-timer'>
                    {expiresAt
                      ? t('{{minutes}} 分 {{seconds}} 秒', remaining)
                      : t('准备中')}
                  </dd>
                </div>
              </dl>

              <div className='payment-checkout-divider' />

              <div className='payment-checkout-assurance'>
                <div>
                  <ShieldCheck size={17} aria-hidden='true' />
                  <Text type='tertiary'>
                    {t('本站将在确认支付结果后更新您的余额或权益。')}
                  </Text>
                </div>
                <div>
                  <Info size={17} aria-hidden='true' />
                  <Text type='tertiary'>
                    {t('如需联系客服，请保留此订单编号。')}
                  </Text>
                </div>
              </div>

              {loadedOrder && error ? (
                <Banner
                  type='warning'
                  closeIcon={null}
                  description={
                    <div className='payment-checkout-inline-error'>
                      <span>{t('自动状态更新已暂停，请刷新后继续。')}</span>
                      <Button
                        size='small'
                        theme='outline'
                        onClick={refreshPayment}
                        loading={refreshing}
                        disabled={refreshing}
                      >
                        {t('刷新状态')}
                      </Button>
                    </div>
                  }
                />
              ) : null}

              {loadedOrder &&
              !error &&
              !polling &&
              status === 'awaiting_payment' ? (
                <Banner
                  type='warning'
                  closeIcon={null}
                  description={t('自动状态更新已暂停，请刷新后继续。')}
                />
              ) : null}
            </aside>
          </div>
        </Card>

        <p className='payment-checkout-footer-note'>
          <QrCode size={14} aria-hidden='true' />
          {t('这是 {{site}} 的支付页面。', { site: systemName })}
        </p>
      </div>
    </main>
  );
};

export default Payment;
