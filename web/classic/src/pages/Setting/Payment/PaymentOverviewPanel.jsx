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

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Banner, Button, Card, Skeleton, Tag } from '@douyinfe/semi-ui';
import {
  Activity,
  Clock3,
  Database,
  RefreshCw,
  ShieldCheck,
} from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { getPaymentOperationsOverview } from './payment-admin-api';
import { getPaymentAdminErrorMessage } from '../../../helpers/payment-admin-errors';
import { formatPaymentAge } from './payment-admin-utils';

const PaymentOverviewPanel = () => {
  const { t, i18n } = useTranslation();
  const [overview, setOverview] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const loadOverview = useCallback(async () => {
    setLoading(true);
    try {
      const data = await getPaymentOperationsOverview();
      setOverview(data);
      setError('');
    } catch (requestError) {
      setError(
        getPaymentAdminErrorMessage(
          requestError,
          t,
          t('Payment overview is unavailable'),
        ),
      );
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void loadOverview();
    const timer = window.setInterval(() => void loadOverview(), 30000);
    return () => window.clearInterval(timer);
  }, [loadOverview]);

  const cards = useMemo(() => {
    if (!overview?.operations) return [];
    const operations = overview.operations;
    const formatter = new Intl.NumberFormat(i18n.language);
    const activeOrders =
      Number(operations.preparing_orders || 0) +
      Number(operations.awaiting_payment_orders || 0) +
      Number(operations.confirming_orders || 0);
    return [
      {
        title: t('Active payment orders'),
        value: formatter.format(activeOrders),
        description: t(
          '{{preparing}} preparing, {{waiting}} waiting, {{confirming}} confirming',
          {
            preparing: formatter.format(operations.preparing_orders || 0),
            waiting: formatter.format(operations.awaiting_payment_orders || 0),
            confirming: formatter.format(operations.confirming_orders || 0),
          },
        ),
        icon: Activity,
      },
      {
        title: t('Creation task backlog'),
        value: formatter.format(operations.create_task_backlog || 0),
        description: t('Oldest task age: {{age}}', {
          age: formatPaymentAge(
            operations.oldest_create_task_age_seconds,
            i18n.language,
          ),
        }),
        icon: Clock3,
      },
      {
        title: t('Orders needing review'),
        value: formatter.format(operations.manual_review_orders || 0),
        description: t('{{events}} unmatched payment events', {
          events: formatter.format(operations.unmatched_payment_events || 0),
        }),
        icon: ShieldCheck,
      },
      {
        title: t('Worker lease health'),
        value: formatter.format(operations.expired_task_leases || 0),
        description: t('{{retrying}} tasks waiting to retry', {
          retrying: formatter.format(operations.retry_waiting_tasks || 0),
        }),
        icon: Database,
      },
    ];
  }, [i18n.language, overview, t]);

  if (loading && !overview) {
    return (
      <div className='grid grid-cols-2 gap-2 sm:gap-3 xl:grid-cols-4'>
        {Array.from({ length: 4 }, (_, index) => (
          <Skeleton
            key={`payment-overview-skeleton-${index}`}
            placeholder={<Skeleton.Title style={{ height: 112 }} />}
            loading
          />
        ))}
      </div>
    );
  }

  if (!overview) {
    return (
      <Banner
        type='danger'
        title={t('Payment overview is unavailable')}
        description={
          <div className='flex flex-wrap items-center justify-between gap-3'>
            <span className='flex flex-col gap-1'>
              <span>
                {t(
                  'The payment data was not changed. Try loading the operational snapshot again.',
                )}
              </span>
              {error ? (
                <span className='font-mono text-xs'>{error}</span>
              ) : null}
            </span>
            <Button onClick={() => void loadOverview()}>{t('Retry')}</Button>
          </div>
        }
        closeIcon={null}
      />
    );
  }

  const { operations, runtime, cluster } = overview;
  const formatter = new Intl.NumberFormat(i18n.language);

  return (
    <div className='flex flex-col gap-4'>
      <div className='flex items-center justify-between gap-3'>
        <div>
          <h3 className='m-0 text-lg font-semibold'>{t('Payment Overview')}</h3>
          <p className='mt-1 mb-0 text-sm text-semi-color-text-2'>
            {t(
              'Operational status for payment orders, workers, callbacks, limits, and cluster readiness.',
            )}
          </p>
        </div>
        <Button
          icon={<RefreshCw size={16} />}
          loading={loading}
          onClick={() => void loadOverview()}
        >
          {t('Refresh')}
        </Button>
      </div>

      {error && (
        <Banner
          type='warning'
          title={error}
          description={t(
            'The last successful operational snapshot remains visible.',
          )}
          closeIcon={null}
        />
      )}

      {!cluster?.ready && (
        <Banner
          className='payment-overview-readiness-banner'
          type='danger'
          title={t('New payment creation is paused')}
          description={t(
            'Cluster readiness check failed: {{code}}. New payments and automatic callback processing are paused; providers should retry after recovery.',
            { code: cluster?.code || t('Not available') },
          )}
          closeIcon={null}
        />
      )}

      <div className='grid grid-cols-2 gap-2 sm:gap-3 xl:grid-cols-4'>
        {cards.map(({ title, value, description, icon: Icon }) => (
          <Card key={title} bodyStyle={{ padding: 12 }}>
            <div className='flex min-w-0 items-start justify-between gap-2'>
              <div className='min-w-0'>
                <div className='text-xs leading-snug font-medium sm:text-sm'>
                  {title}
                </div>
                <div className='mt-2 text-2xl font-semibold tabular-nums'>
                  {value}
                </div>
              </div>
              <Icon size={16} className='shrink-0 text-semi-color-text-2' />
            </div>
            <p className='mt-2 mb-0 text-[11px] leading-4 text-semi-color-text-2 sm:text-xs'>
              {description}
            </p>
          </Card>
        ))}
      </div>

      <Card bodyStyle={{ padding: 16 }}>
        <div className='grid grid-cols-2 gap-x-4 gap-y-4 text-sm sm:gap-x-6 xl:grid-cols-4'>
          <div className='min-w-0'>
            <div className='text-semi-color-text-2'>
              {t('Reconciliation backlog')}
            </div>
            <strong className='mt-1 block tabular-nums'>
              {formatter.format(operations?.reconcile_task_backlog || 0)}
            </strong>
          </div>
          <div className='min-w-0'>
            <div className='text-semi-color-text-2'>
              {t('Running payment tasks')}
            </div>
            <strong className='mt-1 block tabular-nums'>
              {formatter.format(operations?.running_tasks || 0)}
            </strong>
          </div>
          <div className='min-w-0'>
            <div className='text-semi-color-text-2'>
              {t('Unprocessed callbacks')}
            </div>
            <strong className='mt-1 block tabular-nums'>
              {t('{{count}} pending, oldest {{age}}', {
                count: formatter.format(
                  operations?.unprocessed_payment_events || 0,
                ),
                age: formatPaymentAge(
                  operations?.oldest_unprocessed_event_age_seconds,
                  i18n.language,
                ),
              })}
            </strong>
          </div>
          <div className='min-w-0'>
            <div className='text-semi-color-text-2'>
              {t('Active limit reservations')}
            </div>
            <strong className='mt-1 block tabular-nums'>
              {formatter.format(operations?.active_limit_reservations || 0)}
            </strong>
          </div>
          <div className='min-w-0'>
            <div className='text-semi-color-text-2'>{t('Database')}</div>
            <strong className='mt-1 block'>
              {runtime?.database_type || t('Not available')}
            </strong>
          </div>
          <div className='min-w-0'>
            <div className='text-semi-color-text-2'>{t('Shared Redis')}</div>
            <div className='mt-1'>
              <Tag color={runtime?.redis_enabled ? 'green' : 'grey'}>
                {runtime?.redis_enabled ? t('Enabled') : t('Disabled')}
              </Tag>
            </div>
          </div>
          <div className='min-w-0'>
            <div className='text-semi-color-text-2'>
              {t('Payment configuration version')}
            </div>
            <strong className='mt-1 block tabular-nums'>
              {formatter.format(operations?.payment_configuration_version || 0)}
            </strong>
          </div>
          <div className='min-w-0'>
            <div className='text-semi-color-text-2'>
              {t('Expired active limit reservations')}
            </div>
            <strong className='mt-1 block tabular-nums'>
              {formatter.format(
                operations?.expired_active_limit_reservations || 0,
              )}
            </strong>
          </div>
        </div>
      </Card>
    </div>
  );
};

export default PaymentOverviewPanel;
