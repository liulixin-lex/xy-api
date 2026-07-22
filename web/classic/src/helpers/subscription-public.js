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

function normalizePublicPlanRecord(record) {
  const plan = record?.plan;
  if (!plan || typeof plan !== 'object' || Array.isArray(plan)) return null;

  return {
    plan: {
      id: plan.id,
      title: plan.title,
      subtitle: plan.subtitle,
      price_amount: plan.price_amount,
      currency: plan.currency,
      duration_unit: plan.duration_unit,
      duration_value: plan.duration_value,
      custom_seconds: plan.custom_seconds,
      allow_balance_pay: plan.allow_balance_pay,
      max_purchase_per_user: plan.max_purchase_per_user,
      total_amount: plan.total_amount,
      quota_reset_period: plan.quota_reset_period,
      quota_reset_custom_seconds: plan.quota_reset_custom_seconds,
      includes_expanded_access:
        plan.includes_expanded_access === true ||
        (typeof plan.upgrade_group === 'string' &&
          plan.upgrade_group.trim() !== ''),
      external_payment_route_ids: Array.isArray(plan.external_payment_route_ids)
        ? [...plan.external_payment_route_ids]
        : [],
    },
  };
}

function normalizePublicSubscriptionRecord(record) {
  const subscription = record?.subscription;
  if (
    !subscription ||
    typeof subscription !== 'object' ||
    Array.isArray(subscription)
  ) {
    return null;
  }

  return {
    subscription: {
      id: subscription.id,
      plan_id: subscription.plan_id,
      plan_title:
        typeof subscription.plan_title === 'string'
          ? subscription.plan_title
          : '',
      amount_total: subscription.amount_total,
      amount_used: subscription.amount_used,
      start_time: subscription.start_time,
      end_time: subscription.end_time,
      status: subscription.status,
      next_reset_time: subscription.next_reset_time || 0,
    },
  };
}

export function normalizePublicSubscriptionPlans(value) {
  if (!Array.isArray(value)) return [];
  return value.map(normalizePublicPlanRecord).filter(Boolean);
}

export function normalizePublicSubscriptionSelf(value) {
  const data = value && typeof value === 'object' ? value : {};
  const subscriptions = Array.isArray(data.subscriptions)
    ? data.subscriptions.map(normalizePublicSubscriptionRecord).filter(Boolean)
    : [];
  const allSubscriptions = Array.isArray(data.all_subscriptions)
    ? data.all_subscriptions
        .map(normalizePublicSubscriptionRecord)
        .filter(Boolean)
    : [];

  return {
    billing_preference:
      typeof data.billing_preference === 'string'
        ? data.billing_preference
        : 'subscription_first',
    subscriptions,
    all_subscriptions: allSubscriptions,
  };
}
