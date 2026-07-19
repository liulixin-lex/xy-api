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
import React, { useState, useEffect, useMemo, useRef } from 'react';
import {
  Button,
  Modal,
  Table,
  Badge,
  Typography,
  Empty,
  Input,
  Tag,
} from '@douyinfe/semi-ui';
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from '@douyinfe/semi-illustrations';
import { Coins } from 'lucide-react';
import { IconSearch } from '@douyinfe/semi-icons';
import { API, timestamp2string } from '../../../helpers';
import { isAdmin } from '../../../helpers/utils';
import { useIsMobile } from '../../../hooks/common/useIsMobile';
import { formatPaymentMinor } from '../payment-utils';
const { Text } = Typography;

// 状态映射配置
const STATUS_CONFIG = {
  success: { type: 'success', key: '成功' },
  pending: { type: 'warning', key: '待支付' },
  processing: { type: 'primary', key: '处理中' },
  failed: { type: 'danger', key: '失败' },
  expired: { type: 'danger', key: '已过期' },
  manual_review: { type: 'warning', key: '人工复核' },
  refunded: { type: 'tertiary', key: '已退款' },
  refund_pending: { type: 'warning', key: '部分退款' },
  disputed: { type: 'danger', key: '争议中' },
  debt: { type: 'danger', key: '欠款冻结' },
};

// 支付方式映射
const PAYMENT_METHOD_MAP = {
  stripe: 'Stripe',
  creem: 'Creem',
  waffo: 'Waffo',
  waffo_pancake: 'Waffo Pancake',
  xorpay_native: 'XORPay 微信支付',
  xorpay_alipay: 'XORPay 支付宝',
  alipay: '支付宝',
  wxpay: '微信',
};

const TopupHistoryModal = ({ visible, onCancel, t }) => {
  const [loading, setLoading] = useState(false);
  const [topups, setTopups] = useState([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);
  const [keyword, setKeyword] = useState('');
  const [debouncedKeyword, setDebouncedKeyword] = useState('');
  const [error, setError] = useState('');
  const requestRef = useRef({ sequence: 0, controller: null });
  const isMobile = useIsMobile();

  const loadTopups = async (currentPage, currentPageSize) => {
    requestRef.current.controller?.abort?.();
    const controller = new AbortController();
    const sequence = requestRef.current.sequence + 1;
    requestRef.current = { sequence, controller };
    setLoading(true);
    setError('');
    try {
      const base = isAdmin() ? '/api/user/topup' : '/api/user/topup/self';
      const qs =
        `p=${currentPage}&page_size=${currentPageSize}` +
        (debouncedKeyword
          ? `&keyword=${encodeURIComponent(debouncedKeyword)}`
          : '');
      const endpoint = `${base}?${qs}`;
      const res = await API.get(endpoint, { signal: controller.signal });
      if (sequence !== requestRef.current.sequence) return;
      const { success, message, data } = res.data;
      if (success) {
        setTopups(data.items || []);
        setTotal(data.total || 0);
      } else {
        setTopups([]);
        setTotal(0);
        setError(message || t('加载账单失败'));
      }
    } catch (requestError) {
      if (requestError?.code === 'ERR_CANCELED') return;
      if (sequence === requestRef.current.sequence) {
        setTopups([]);
        setTotal(0);
        setError(t('加载账单失败'));
      }
    } finally {
      if (sequence === requestRef.current.sequence) setLoading(false);
    }
  };

  useEffect(() => {
    if (visible) {
      loadTopups(page, pageSize);
    }
    return () => requestRef.current.controller?.abort?.();
  }, [visible, page, pageSize, debouncedKeyword]);

  useEffect(() => {
    const timer = window.setTimeout(
      () => setDebouncedKeyword(keyword.trim()),
      300,
    );
    return () => window.clearTimeout(timer);
  }, [keyword]);

  const handlePageChange = (currentPage) => {
    setPage(currentPage);
  };

  const handlePageSizeChange = (currentPageSize) => {
    setPageSize(currentPageSize);
    setPage(1);
  };

  const handleKeywordChange = (value) => {
    setKeyword(value);
    setPage(1);
  };

  // 渲染状态徽章
  const renderStatusBadge = (status) => {
    const config = STATUS_CONFIG[status] || { type: 'primary', key: status };
    return (
      <span className='flex items-center gap-2'>
        <Badge dot type={config.type} />
        <span>{t(config.key)}</span>
      </span>
    );
  };

  // 渲染支付方式
  const renderPaymentMethod = (pm) => {
    const displayName = PAYMENT_METHOD_MAP[pm];
    return <Text>{displayName ? t(displayName) : pm || '-'}</Text>;
  };

  const isSubscriptionTopup = (record) => {
    if (record?.order_kind) return record.order_kind === 'subscription';
    const tradeNo = (record?.trade_no || '').toLowerCase();
    return Number(record?.amount || 0) === 0 && tradeNo.startsWith('sub');
  };

  const formatPayment = (record) => {
    if (typeof record?.expected_amount_minor !== 'number') {
      return `¥${Number(record?.money || 0).toFixed(2)}`;
    }
    const minor = record.paid_amount_minor || record.expected_amount_minor;
    return formatPaymentMinor(
      minor,
      record.currency,
      record.payment_provider || record.provider,
    );
  };

  // 检查是否为管理员
  const userIsAdmin = useMemo(() => isAdmin(), []);

  const columns = useMemo(() => {
    const baseColumns = [
      ...(userIsAdmin
        ? [
            {
              title: t('用户ID'),
              dataIndex: 'user_id',
              key: 'user_id',
              render: (userId) => <Text>{userId ?? '-'}</Text>,
            },
          ]
        : []),
      {
        title: t('订单号'),
        dataIndex: 'trade_no',
        key: 'trade_no',
        render: (text) => <Text copyable>{text}</Text>,
      },
      {
        title: t('支付网关'),
        dataIndex: 'payment_provider',
        key: 'payment_provider',
        render: (_, record) => (
          <Text>{record.payment_provider || record.provider || '-'}</Text>
        ),
      },
      {
        title: t('支付方式'),
        dataIndex: 'payment_method',
        key: 'payment_method',
        render: renderPaymentMethod,
      },
      {
        title: t('充值额度'),
        dataIndex: 'amount',
        key: 'amount',
        render: (amount, record) => {
          if (isSubscriptionTopup(record)) {
            return (
              <Tag color='purple' shape='circle' size='small'>
                {t('订阅套餐')}
              </Tag>
            );
          }
          return (
            <span className='flex items-center gap-1'>
              <Coins size={16} />
              <Text>{amount}</Text>
            </span>
          );
        },
      },
      {
        title: t('支付金额'),
        dataIndex: 'money',
        key: 'money',
        render: (_, record) => (
          <Text type='danger'>{formatPayment(record)}</Text>
        ),
      },
      {
        title: t('状态'),
        dataIndex: 'status',
        key: 'status',
        render: renderStatusBadge,
      },
    ];

    baseColumns.push({
      title: t('创建时间'),
      dataIndex: 'create_time',
      key: 'create_time',
      render: (time) => timestamp2string(time),
    });

    return baseColumns;
  }, [t, userIsAdmin]);

  return (
    <Modal
      title={t('充值账单')}
      visible={visible}
      onCancel={onCancel}
      footer={null}
      size={isMobile ? 'full-width' : 'large'}
    >
      <div className='mb-3'>
        <Input
          prefix={<IconSearch />}
          placeholder={t('订单号')}
          aria-label={t('搜索订单号')}
          value={keyword}
          onChange={handleKeywordChange}
          showClear
        />
      </div>
      <Table
        columns={columns}
        dataSource={topups}
        loading={loading}
        rowKey='id'
        pagination={{
          currentPage: page,
          pageSize: pageSize,
          total: total,
          showSizeChanger: true,
          pageSizeOpts: [10, 20, 50, 100],
          onPageChange: handlePageChange,
          onPageSizeChange: handlePageSizeChange,
        }}
        size='small'
        empty={
          error ? (
            <Empty
              image={
                <IllustrationNoResult style={{ width: 150, height: 150 }} />
              }
              darkModeImage={
                <IllustrationNoResultDark style={{ width: 150, height: 150 }} />
              }
              description={
                <div className='flex flex-col items-center gap-2'>
                  <Text strong>{t('账单加载失败')}</Text>
                  <span>{error}</span>
                  <Button
                    size='small'
                    theme='outline'
                    onClick={() => loadTopups(page, pageSize)}
                  >
                    {t('重新加载')}
                  </Button>
                </div>
              }
              style={{ padding: 30 }}
            />
          ) : (
            <Empty
              image={
                <IllustrationNoResult style={{ width: 150, height: 150 }} />
              }
              darkModeImage={
                <IllustrationNoResultDark style={{ width: 150, height: 150 }} />
              }
              description={
                debouncedKeyword ? t('未找到匹配账单') : t('暂无充值记录')
              }
              style={{ padding: 30 }}
            />
          )
        }
      />
    </Modal>
  );
};

export default TopupHistoryModal;
