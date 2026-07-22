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
import {
  Banner,
  Button,
  Card,
  Modal,
  Pagination,
  Spin,
  Tag,
  TextArea,
} from '@douyinfe/semi-ui';
import { RefreshCw } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { showError, showSuccess, showWarning } from '../../../helpers';
import { getPaymentAdminErrorMessage } from '../../../helpers/payment-admin-errors';
import {
  cancelStripeLegacySubscriptionAtPeriodEnd,
  listStripeLegacyInventory,
  syncStripeLegacyInventory,
} from './payment-admin-api';
import {
  canScheduleStripeSubscriptionCancellation,
  isStripeCancellationReasonValid,
} from './stripe-legacy-cancellation';

const PAGE_SIZE = 20;

const StripeLegacyInventoryPanel = ({ withPaymentVerification }) => {
  const { t, i18n } = useTranslation();
  const [inventory, setInventory] = useState(null);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [inventoryError, setInventoryError] = useState('');
  const [cancellationItem, setCancellationItem] = useState(null);
  const [cancellationReason, setCancellationReason] = useState('');
  const [cancellationPending, setCancellationPending] = useState(false);
  const cancellationReasonByteLength = new TextEncoder().encode(
    cancellationReason.trim(),
  ).length;

  const loadInventory = useCallback(
    async (nextPage = 1) => {
      setLoading(true);
      try {
        const data = await listStripeLegacyInventory({
          page: nextPage,
          pageSize: PAGE_SIZE,
        });
        setInventory(data);
        setPage(nextPage);
        setInventoryError('');
        return true;
      } catch (error) {
        setInventoryError(
          getPaymentAdminErrorMessage(
            error,
            t,
            t('Stripe legacy inventory is unavailable. Try again.'),
          ),
        );
        return false;
      } finally {
        setLoading(false);
      }
    },
    [t],
  );

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
        getPaymentAdminErrorMessage(
          error,
          t,
          t(
            'Stripe legacy inventory sync is temporarily unavailable. Try again later.',
          ),
        ),
      );
    } finally {
      setSyncing(false);
    }
  };

  const closeCancellationDialog = () => {
    if (cancellationPending) return;
    setCancellationItem(null);
    setCancellationReason('');
  };

  const scheduleCancellation = async () => {
    if (
      cancellationPending ||
      !canScheduleStripeSubscriptionCancellation(cancellationItem) ||
      !isStripeCancellationReasonValid(cancellationReason)
    ) {
      return;
    }
    const item = cancellationItem;
    const reason = cancellationReason.trim();
    setCancellationPending(true);
    try {
      await withPaymentVerification(async () => {
        let result;
        try {
          result = await cancelStripeLegacySubscriptionAtPeriodEnd({
            inventory_id: item.id,
            expected_updated_at: item.expected_updated_at,
            reason,
          });
        } catch (error) {
          const code = error?.response?.data?.code || error?.code;
          if (
            code === 'stripe_inventory_cancel_conflict' ||
            code === 'stripe_inventory_subscription_not_found'
          ) {
            void loadInventory(page);
          }
          throw error;
        }
        setCancellationItem(null);
        setCancellationReason('');
        const refreshed = await loadInventory(page);
        if (refreshed) {
          showSuccess(
            result.duplicate
              ? t('Stripe cancellation was already scheduled')
              : t('Stripe cancellation scheduled for the period end'),
          );
        } else {
          showWarning(
            t(
              'Stripe accepted the cancellation, but the refreshed inventory could not be loaded.',
            ),
          );
        }
        return result;
      });
    } catch (error) {
      showError(
        getPaymentAdminErrorMessage(
          error,
          t,
          t('Failed to schedule Stripe subscription cancellation'),
        ),
      );
    } finally {
      setCancellationPending(false);
    }
  };

  if (loading && !inventory) {
    return (
      <div className='mt-6'>
        <Card>
          <Spin spinning>
            <div className='flex min-h-32 items-center justify-center text-sm text-semi-color-text-2'>
              {t('Loading Stripe legacy inventory...')}
            </div>
          </Spin>
        </Card>
      </div>
    );
  }

  if (inventoryError && !inventory) {
    return (
      <Banner
        type='warning'
        title={inventoryError}
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

  if (!inventory || Number(inventory.total || 0) <= 0) {
    return (
      <div className='mt-6'>
        <Banner
          type='info'
          title={t('No legacy Stripe subscriptions found')}
          description={
            <div className='flex flex-wrap items-center justify-between gap-3'>
              <span>
                {t(
                  'No recurring Stripe subscriptions are currently recorded. Sync from Stripe to refresh the inventory.',
                )}
              </span>
              <Button
                icon={<RefreshCw size={16} />}
                loading={syncing}
                onClick={() => void syncInventory()}
              >
                {t('Sync from Stripe')}
              </Button>
            </div>
          }
          closeIcon={null}
        />
      </div>
    );
  }

  const items = Array.isArray(inventory.items) ? inventory.items : [];
  const dateFormatter = new Intl.DateTimeFormat(i18n.language, {
    dateStyle: 'medium',
    timeStyle: 'short',
  });

  return (
    <div className='mt-6 flex flex-col gap-4'>
      {inventoryError ? (
        <Banner
          type='warning'
          title={inventoryError}
          description={t(
            'The last successful operational snapshot remains visible.',
          )}
          closeIcon={null}
        />
      ) : null}
      <Banner
        type='warning'
        title={t('Legacy Stripe subscription controls')}
        description={t(
          'Inventory sync only observes legacy recurring subscriptions. Verified cancellation can stop a future Stripe renewal but never grants, renews, refunds, or revokes local access.',
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
        <div className='border-b px-4 py-3 text-sm text-semi-color-text-2'>
          {t(
            'Synchronizing reads legacy subscription state from Stripe. It does not change renewals, issue refunds, or change local access.',
          )}
        </div>
        <Spin spinning={loading}>
          <div className='overflow-x-auto'>
            <table className='w-full min-w-[1020px] border-collapse text-sm'>
              <thead>
                <tr className='border-b text-left text-semi-color-text-2'>
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
                  <th className='px-4 py-3 text-right font-medium'>
                    {t('Actions')}
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
                      <div className='mt-1 text-xs text-semi-color-text-2'>
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
                    <td className='px-4 py-3 text-right'>
                      <Button
                        size='small'
                        theme='outline'
                        type='danger'
                        disabled={
                          !canScheduleStripeSubscriptionCancellation(item)
                        }
                        title={
                          canScheduleStripeSubscriptionCancellation(item)
                            ? t('Cancel renewal at period end')
                            : t(
                                'Cancellation is unavailable for this subscription snapshot',
                              )
                        }
                        onClick={() => {
                          setCancellationItem(item);
                          setCancellationReason('');
                        }}
                      >
                        {t('Cancel renewal')}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Spin>
        <div className='flex items-center justify-between gap-3 px-4 py-3'>
          <span className='text-sm text-semi-color-text-2'>
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

      <Modal
        visible={Boolean(cancellationItem)}
        title={t('Cancel Stripe renewal at period end?')}
        centered
        maskClosable={false}
        confirmLoading={cancellationPending}
        okType='danger'
        okText={
          cancellationPending
            ? t('Scheduling cancellation...')
            : t('Cancel at period end')
        }
        cancelText={t('Keep subscription active')}
        okButtonProps={{
          disabled:
            cancellationPending ||
            !canScheduleStripeSubscriptionCancellation(cancellationItem) ||
            !isStripeCancellationReasonValid(cancellationReason),
        }}
        onOk={() => void scheduleCancellation()}
        onCancel={closeCancellationDialog}
      >
        <div className='flex flex-col gap-4'>
          <p className='m-0'>
            {t(
              'Stripe will stop renewing this legacy subscription after its current billing period.',
            )}
          </p>
          <Banner
            type='warning'
            title={t('Stripe renewal only')}
            description={t(
              'This changes Stripe renewal state only. It does not issue a refund or change local access.',
            )}
            closeIcon={null}
          />
          <dl className='m-0 grid gap-3 rounded border border-solid border-semi-color-border p-3 text-sm'>
            <div>
              <dt className='text-xs text-semi-color-text-2'>
                {t('Stripe Subscription ID')}
              </dt>
              <dd className='mt-1 ml-0 truncate font-mono'>
                {cancellationItem?.stripe_subscription_id || '-'}
              </dd>
            </div>
            <div>
              <dt className='text-xs text-semi-color-text-2'>
                {t('Current period ends')}
              </dt>
              <dd className='mt-1 ml-0'>
                {cancellationItem?.current_period_end
                  ? dateFormatter.format(
                      cancellationItem.current_period_end * 1000,
                    )
                  : '—'}
              </dd>
            </div>
          </dl>
          <label
            className='text-sm font-medium'
            htmlFor='classic-stripe-cancellation-reason'
          >
            {t('Cancellation reason')}
          </label>
          <TextArea
            id='classic-stripe-cancellation-reason'
            value={cancellationReason}
            autosize={{ minRows: 3, maxRows: 6 }}
            disabled={cancellationPending}
            placeholder={t(
              'Explain why this legacy renewal should stop (8-512 UTF-8 bytes)',
            )}
            onChange={setCancellationReason}
          />
          <div className='flex flex-wrap justify-between gap-2 text-xs'>
            <span
              className={
                cancellationReasonByteLength > 0 &&
                !isStripeCancellationReasonValid(cancellationReason)
                  ? 'text-semi-color-danger'
                  : 'text-semi-color-text-2'
              }
            >
              {cancellationReasonByteLength > 0 &&
              !isStripeCancellationReasonValid(cancellationReason)
                ? t('Reason must be between 8 and 512 UTF-8 bytes')
                : t('This reason is stored in the payment operations audit.')}
            </span>
            <span className='text-semi-color-text-2 tabular-nums'>
              {cancellationReasonByteLength} / 512
            </span>
          </div>
        </div>
      </Modal>
    </div>
  );
};

export default StripeLegacyInventoryPanel;
