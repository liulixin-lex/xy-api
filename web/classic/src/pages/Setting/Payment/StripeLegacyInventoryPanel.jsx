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
import { Banner, Button, Card, Pagination, Spin, Tag } from '@douyinfe/semi-ui';
import { RefreshCw } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { showError, showSuccess, showWarning } from '../../../helpers';
import {
  listStripeLegacyInventory,
  syncStripeLegacyInventory,
} from './payment-admin-api';

const PAGE_SIZE = 20;

const StripeLegacyInventoryPanel = ({ withPaymentVerification }) => {
  const { t, i18n } = useTranslation();
  const [inventory, setInventory] = useState(null);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [presenceError, setPresenceError] = useState(false);

  const loadInventory = useCallback(async (nextPage = 1) => {
    setLoading(true);
    try {
      const data = await listStripeLegacyInventory({
        page: nextPage,
        pageSize: PAGE_SIZE,
      });
      setInventory(data);
      setPage(nextPage);
      setPresenceError(false);
      return true;
    } catch {
      setPresenceError(true);
      return false;
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadInventory(1);
  }, [loadInventory]);

  const syncInventory = async () => {
    if (syncing) return;
    setSyncing(true);
    try {
      await withPaymentVerification(async () => {
        const result = await syncStripeLegacyInventory();
        const refreshed = await loadInventory(1);
        if (refreshed) {
          showSuccess(
            t(
              'Stripe inventory sync completed: {{seen}} seen, {{mapped}} mapped, {{unmapped}} unmapped.',
              {
                seen: result.seen,
                mapped: result.mapped,
                unmapped: result.unmapped,
              },
            ),
          );
        } else {
          showWarning(
            t(
              'Stripe inventory was synchronized, but the refreshed inventory could not be loaded.',
            ),
          );
        }
        return result;
      });
    } catch (error) {
      showError(
        error?.message || t('Failed to sync Stripe subscription inventory'),
      );
    } finally {
      setSyncing(false);
    }
  };

  if (loading && !inventory) return null;

  if (presenceError && !inventory) {
    return (
      <Banner
        type='warning'
        title={t('Legacy Stripe history could not be checked')}
        description={
          <div className='flex flex-wrap items-center justify-between gap-3'>
            <span>
              {t(
                'Current one-time Stripe Checkout settings are unchanged. Retry to determine whether legacy recurring subscription records exist.',
              )}
            </span>
            <Button onClick={() => void loadInventory(1)}>{t('Retry')}</Button>
          </div>
        }
        closeIcon={null}
        style={{ marginTop: 16 }}
      />
    );
  }

  if (!inventory || Number(inventory.total || 0) <= 0) return null;

  const items = Array.isArray(inventory.items) ? inventory.items : [];
  const dateFormatter = new Intl.DateTimeFormat(i18n.language, {
    dateStyle: 'medium',
    timeStyle: 'short',
  });

  return (
    <div className='mt-6 flex flex-col gap-4'>
      <Banner
        type='warning'
        title={t('Read-only Stripe legacy subscription history')}
        description={t(
          'This inventory observes old recurring Stripe subscriptions only. It does not grant, renew, cancel, or revoke local access. Current unified Stripe top-ups and fixed-term access purchases use one-time Checkout and do not auto-renew.',
        )}
        closeIcon={null}
      />
      <Card
        title={t('Stripe Legacy Inventory')}
        headerExtraContent={
          <Button
            icon={<RefreshCw size={16} />}
            loading={syncing}
            onClick={() => void syncInventory()}
          >
            {t('Sync from Stripe')}
          </Button>
        }
        bodyStyle={{ padding: 0 }}
      >
        <div className='border-b px-4 py-3 text-sm text-gray-500'>
          {t(
            'Synchronizing reads legacy subscription state from Stripe. It does not cancel subscriptions, close Checkout Sessions, issue refunds, or revoke Stripe API keys.',
          )}
        </div>
        <Spin spinning={loading}>
          <div className='overflow-x-auto'>
            <table className='w-full min-w-[900px] border-collapse text-sm'>
              <thead>
                <tr className='border-b text-left text-gray-500'>
                  <th className='px-4 py-3 font-medium'>
                    {t('Stripe Subscription ID')}
                  </th>
                  <th className='px-4 py-3 font-medium'>
                    {t('Stripe Customer ID')}
                  </th>
                  <th className='px-4 py-3 font-medium'>{t('Mapping')}</th>
                  <th className='px-4 py-3 font-medium'>{t('Status')}</th>
                  <th className='px-4 py-3 font-medium'>
                    {t('Current period ends')}
                  </th>
                  <th className='px-4 py-3 font-medium'>
                    {t('Last observed')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => (
                  <tr key={item.stripe_subscription_id} className='border-b'>
                    <td className='px-4 py-3 font-mono text-xs'>
                      {item.stripe_subscription_id}
                    </td>
                    <td className='px-4 py-3 font-mono text-xs'>
                      {item.stripe_customer_id}
                    </td>
                    <td className='px-4 py-3'>
                      <Tag
                        color={
                          item.mapping_status === 'mapped' ? 'green' : 'orange'
                        }
                      >
                        {item.mapping_status || '-'}
                      </Tag>
                      <div className='mt-1 text-xs text-gray-500'>
                        {item.user_id ? `#${item.user_id}` : t('User unmapped')}
                        {' · '}
                        {item.subscription_plan_id
                          ? `${t('Plan')} #${item.subscription_plan_id}`
                          : t('Plan unmapped')}
                      </div>
                    </td>
                    <td className='px-4 py-3'>
                      <Tag>{item.status || '-'}</Tag>
                      {item.cancel_at_period_end && (
                        <div className='mt-1 text-xs text-orange-600'>
                          {t('Cancels at period end')}
                        </div>
                      )}
                    </td>
                    <td className='px-4 py-3'>
                      {item.current_period_end
                        ? dateFormatter.format(item.current_period_end * 1000)
                        : '—'}
                    </td>
                    <td className='px-4 py-3'>
                      {item.state_observed_at
                        ? dateFormatter.format(item.state_observed_at * 1000)
                        : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Spin>
        <div className='flex items-center justify-between gap-3 px-4 py-3'>
          <span className='text-sm text-gray-500'>
            {t('{{count}} legacy Stripe records', {
              count: Number(inventory.total || 0),
            })}
          </span>
          <Pagination
            currentPage={page}
            pageSize={PAGE_SIZE}
            total={Number(inventory.total || 0)}
            showSizeChanger={false}
            onPageChange={(nextPage) => void loadInventory(nextPage)}
          />
        </div>
      </Card>
    </div>
  );
};

export default StripeLegacyInventoryPanel;
