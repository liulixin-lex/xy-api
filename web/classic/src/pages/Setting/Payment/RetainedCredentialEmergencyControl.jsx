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
import React, { useCallback, useState } from 'react';
import {
  Banner,
  Button,
  Modal,
  TextArea,
  Toast,
  Typography,
} from '@douyinfe/semi-ui';
import { TriangleAlert } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { API } from '../../../helpers';
import {
  createPaymentAdminError,
  getRetainedCredentialDisableErrorMessage,
} from '../../../helpers/payment-admin-errors';
import { isEmergencyCredentialRevocationReasonValid } from '../../../helpers/payment-credential-revocation';
import {
  buildRetainedCredentialDisablePayload,
  buildRetainedCredentialDisablePreviewParams,
} from '../../../helpers/retained-payment-credential-disable';
import { isVerificationRequiredError } from '../../../helpers/secureApiCall';

const { Text } = Typography;

const hasHttpStatus = (error, status) => error?.response?.status === status;

export default function RetainedCredentialEmergencyControl(props) {
  const { t } = useTranslation();
  const [visible, setVisible] = useState(false);
  const [preview, setPreview] = useState(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState('');
  const [reason, setReason] = useState('');
  const [actionPending, setActionPending] = useState(false);

  let providerLabel = t('Creem');
  let emergencyScope = t(
    'This clears the current Creem API key and webhook signing secret in this system. Products, limits, and other non-credential settings remain unchanged.',
  );
  if (props.provider === 'waffo') {
    providerLabel = t('Waffo');
    emergencyScope = t(
      'This disables Waffo and clears the API key, private key, and callback verification certificate for the currently selected environment. Merchant, payment-method, limit, and routing settings remain unchanged.',
    );
  } else if (props.provider === 'waffo_pancake') {
    providerLabel = t('Waffo Pancake');
    emergencyScope = t(
      'This clears the current Waffo Pancake private key and Store binding in this system so checkout and webhook attribution fail closed. Merchant, Product, return, and limit settings remain unchanged.',
    );
  }

  const resetDialog = useCallback(() => {
    setPreview(null);
    setPreviewError('');
    setReason('');
  }, []);

  const loadPreview = useCallback(async () => {
    setPreview(null);
    setPreviewError('');
    setPreviewLoading(true);
    try {
      const response = await API.get(
        '/api/option/payment/credential-revocation-preview',
        {
          params: buildRetainedCredentialDisablePreviewParams(props.provider),
          skipErrorHandler: true,
        },
      );
      if (!response.data?.success || !response.data?.data) {
        const error = createPaymentAdminError(
          response.data,
          t('The emergency impact preview could not be loaded.'),
        );
        setPreviewError(
          getRetainedCredentialDisableErrorMessage(
            error,
            t,
            t('The emergency impact preview could not be loaded.'),
          ),
        );
        return;
      }
      setPreview(response.data.data);
    } catch (error) {
      setPreviewError(
        getRetainedCredentialDisableErrorMessage(
          error,
          t,
          t('The emergency impact preview could not be loaded.'),
        ),
      );
    } finally {
      setPreviewLoading(false);
    }
  }, [props.provider, t]);

  const openPreview = () => {
    resetDialog();
    setVisible(true);
    void loadPreview();
  };

  const reasonValid = isEmergencyCredentialRevocationReasonValid(reason);
  const controlsDisabled = props.disabled || actionPending;

  const disableCurrentCredential = async () => {
    if (!preview || !reasonValid || controlsDisabled) return;
    const toastId = `retained-credential-disable-${props.provider}`;
    const operation = async () => {
      setActionPending(true);
      Toast.info({
        id: toastId,
        content: t('Disabling {{provider}} current credentials...', {
          provider: providerLabel,
        }),
        duration: 0,
      });
      try {
        const response = await API.put(
          '/api/option/payment',
          buildRetainedCredentialDisablePayload(
            props.provider,
            reason,
            preview.configuration_version,
          ),
          { skipErrorHandler: true },
        );
        if (!response.data?.success) {
          const error = createPaymentAdminError(
            response.data,
            t('Current credentials could not be disabled.'),
          );
          Toast.error({
            id: toastId,
            content: getRetainedCredentialDisableErrorMessage(
              error,
              t,
              t('Current credentials could not be disabled.'),
            ),
          });
          return null;
        }

        const refreshed = await props.onCompleted(response.data);
        setVisible(false);
        resetDialog();
        if (refreshed) {
          Toast.success({
            id: toastId,
            content: t(
              '{{provider}} current credentials disabled; affected orders quarantined',
              { provider: providerLabel },
            ),
          });
        } else {
          Toast.warning({
            id: toastId,
            content: t(
              '{{provider}} current credentials were disabled, but the latest status could not be refreshed.',
              { provider: providerLabel },
            ),
          });
        }
        return response;
      } catch (error) {
        if (isVerificationRequiredError(error)) {
          Toast.close(toastId);
          throw error;
        }
        if (hasHttpStatus(error, 409)) {
          await props.onStale?.();
        }
        Toast.error({
          id: toastId,
          content: getRetainedCredentialDisableErrorMessage(
            error,
            t,
            t('Current credentials could not be disabled.'),
          ),
        });
        return null;
      } finally {
        setActionPending(false);
      }
    };

    await props.withPaymentVerification(operation, {
      preferredMethod: 'passkey',
      title: t('Verify emergency credential disable'),
      description: t(
        'Confirm your identity before disabling current payment credentials and quarantining affected orders.',
      ),
    });
  };

  const impact = preview?.impact;
  const impactItems = impact
    ? [
        [t('Total affected orders'), impact.total_affected_orders],
        [
          t('Unfinished orders moving to manual review'),
          impact.total_unfinished_orders,
        ],
        [t('Canonical payment orders'), impact.canonical_affected_orders],
        [t('Legacy pending top-ups'), impact.legacy_pending_topups],
        [
          t('Legacy pending subscriptions'),
          impact.legacy_pending_subscriptions,
        ],
        [t('Unmatched economic events'), impact.unmatched_economic_events],
      ]
    : [];

  return (
    <section
      className='mt-6 border-t border-solid border-semi-color-border pt-5'
      aria-label={t('Advanced and emergency')}
    >
      <div className='mb-3'>
        <Text strong>{t('Advanced and emergency')}</Text>
        <p className='mt-1 mb-0 max-w-3xl text-sm text-semi-color-text-2'>
          {t(
            'You can replace these credentials through normal save only when no dependent orders still require the current credentials.',
          )}
        </p>
      </div>

      <Banner
        type='warning'
        icon={<TriangleAlert size={16} />}
        title={t('Emergency credential disable')}
        description={t(
          'If the current credential may be compromised, review the impact before disabling it and quarantining affected orders for manual review.',
        )}
        closeIcon={null}
      />
      <Button
        className='mt-3'
        type='danger'
        disabled={controlsDisabled}
        onClick={openPreview}
      >
        {t('Review impact and disable')}
      </Button>

      <Modal
        visible={visible}
        title={t('Disable {{provider}} current credentials?', {
          provider: providerLabel,
        })}
        centered
        maskClosable={false}
        confirmLoading={actionPending}
        okType='danger'
        okText={
          actionPending
            ? t('Disabling and quarantining...')
            : t('Disable credentials and quarantine orders')
        }
        cancelText={t('取消')}
        okButtonProps={{
          disabled: controlsDisabled || !preview || !reasonValid,
        }}
        onOk={() => void disableCurrentCredential()}
        onCancel={() => {
          if (controlsDisabled) return;
          setVisible(false);
          resetDialog();
        }}
      >
        <div className='flex flex-col gap-4'>
          <p className='m-0'>{emergencyScope}</p>
          <Banner
            type='warning'
            title={t('Emergency disable does not replace credentials')}
            description={t(
              'This emergency action does not accept replacement credentials. It immediately stops using the current credentials and moves affected unfinished orders to manual review.',
            )}
            closeIcon={null}
          />

          {previewLoading ? (
            <div
              className='rounded border border-solid border-semi-color-border bg-semi-color-fill-0 p-4 text-sm text-semi-color-text-2'
              role='status'
              aria-live='polite'
            >
              {t('Loading emergency impact preview...')}
            </div>
          ) : previewError ? (
            <Banner
              type='danger'
              title={t('Impact preview unavailable')}
              description={
                <div className='flex flex-col items-start gap-2'>
                  <span>{previewError}</span>
                  <Button
                    size='small'
                    theme='outline'
                    disabled={controlsDisabled}
                    onClick={() => void loadPreview()}
                  >
                    {t('Retry preview')}
                  </Button>
                </div>
              }
              closeIcon={null}
            />
          ) : preview && impact ? (
            <div className='rounded border border-solid border-semi-color-border p-4'>
              <Text strong>{t('Affected order preview')}</Text>
              <p className='mt-1 mb-3 text-xs text-semi-color-text-2'>
                {t('Preview generated at {{time}}', {
                  time: new Date(preview.generated_at * 1000).toLocaleString(),
                })}
              </p>
              <dl className='m-0 grid grid-cols-1 gap-3 sm:grid-cols-2'>
                {impactItems.map(([label, value]) => (
                  <div key={label} className='rounded bg-semi-color-fill-0 p-3'>
                    <dt className='text-xs text-semi-color-text-2'>{label}</dt>
                    <dd className='mt-1 ml-0 text-lg font-semibold tabular-nums'>
                      {Number(value).toLocaleString()}
                    </dd>
                  </div>
                ))}
              </dl>
            </div>
          ) : null}

          <label
            className='text-sm font-medium'
            htmlFor={`classic-retained-credential-disable-reason-${props.provider}`}
          >
            {t('Emergency disable reason')}
          </label>
          <TextArea
            id={`classic-retained-credential-disable-reason-${props.provider}`}
            value={reason}
            maxCount={512}
            autosize={{ minRows: 3, maxRows: 6 }}
            disabled={controlsDisabled || !preview}
            placeholder={t(
              'Describe why the current credentials must be disabled and how the incident is being handled',
            )}
            onChange={setReason}
          />
          <div className='flex flex-wrap justify-between gap-2 text-xs'>
            <span
              className={
                reason.length > 0 && !reasonValid
                  ? 'text-semi-color-danger'
                  : 'text-semi-color-text-2'
              }
            >
              {reason.length > 0 && !reasonValid
                ? t('Reason must be between 8 and 512 characters')
                : t(
                    'Enter 8 to 512 characters explaining the credential incident and response.',
                  )}
            </span>
            <span className='text-semi-color-text-2 tabular-nums'>
              {reason.length} / 512
            </span>
          </div>
        </div>
      </Modal>
    </section>
  );
}
