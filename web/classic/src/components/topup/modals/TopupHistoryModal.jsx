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
} from '@douyinfe/semi-ui';
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from '@douyinfe/semi-illustrations';
import { Coins } from 'lucide-react';
import { IconSearch } from '@douyinfe/semi-icons';
import { API, timestamp2string } from '../../../helpers';
import { useIsMobile } from '../../../hooks/common/useIsMobile';
import {
  formatPaymentDecimal,
  getPublicPaymentMethodLabel,
  normalizePublicTopupRecord,
  normalizePublicPaymentStatus,
} from '../payment-utils';
const { Text } = Typography;

// 状态映射配置
const PUBLIC_STATUS_CONFIG = {
  preparing: { type: 'primary', key: '支付准备中' },
  awaiting_payment: { type: 'warning', key: '等待支付' },
  confirming: { type: 'primary', key: '确认中' },
  succeeded: { type: 'success', key: '支付成功' },
  expired: { type: 'warning', key: '已过期' },
  temporarily_unavailable: { type: 'danger', key: '暂时不可用' },
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
      const base = '/api/user/topup/self';
      const qs =
        `p=${currentPage}&page_size=${currentPageSize}` +
        (debouncedKeyword
          ? `&keyword=${encodeURIComponent(debouncedKeyword)}`
          : '');
      const endpoint = `${base}?${qs}`;
      const res = await API.get(endpoint, {
        signal: controller.signal,
        skipErrorHandler: true,
      });
      if (sequence !== requestRef.current.sequence) return;
      const { success, data } = res.data;
      if (success) {
        const safeItems = Array.isArray(data?.items)
          ? data.items
              .map(normalizePublicTopupRecord)
              .filter((item) => item !== null)
          : [];
        setTopups(safeItems);
        setTotal(data.total || 0);
      } else {
        setTopups([]);
        setTotal(0);
        setError(t('加载账单失败'));
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
  const renderStatusBadge = (status, record) => {
    const publicStatus = normalizePublicPaymentStatus({
      status_code: record?.status_code,
      status,
    });
    const config =
      PUBLIC_STATUS_CONFIG[publicStatus] ||
      PUBLIC_STATUS_CONFIG.temporarily_unavailable;
    return (
      <span className='flex items-center gap-2'>
        <Badge dot type={config.type} />
        <span>{t(config.key)}</span>
      </span>
    );
  };

  // 渲染支付方式
  const renderPaymentMethod = (pm, record) => {
    return <Text>{getPublicPaymentMethodLabel(record, t)}</Text>;
  };

  const formatPayment = (record) => {
    return formatPaymentDecimal(
      record?.payment_amount,
      record?.currency,
      record?.public_method,
    );
  };

  const columns = useMemo(() => {
    const baseColumns = [
      {
        title: t('订单号'),
        dataIndex: 'trade_no',
        key: 'trade_no',
        render: (text) => <Text copyable>{text}</Text>,
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
        render: (amount) => (
          <span className='flex items-center gap-1'>
            <Coins size={16} />
            <Text>{amount}</Text>
          </span>
        ),
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
        render: (status, record) => renderStatusBadge(status, record),
      },
    ];

    baseColumns.push({
      title: t('创建时间'),
      dataIndex: 'created_at',
      key: 'created_at',
      render: (time) => timestamp2string(time),
    });

    return baseColumns;
  }, [t]);

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
