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

import React, { useCallback } from 'react';
import { Card, Tabs } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';

import PaymentOverviewPanel from '../../pages/Setting/Payment/PaymentOverviewPanel';
import StripeLegacyInventoryPanel from '../../pages/Setting/Payment/StripeLegacyInventoryPanel';
import SecureVerificationModal from '../common/modals/SecureVerificationModal';
import { useSecureVerification } from '../../hooks/common/useSecureVerification';

const PaymentOperationsSetting = () => {
  const { t } = useTranslation();
  const {
    isModalVisible: paymentVerificationVisible,
    verificationMethods: paymentVerificationMethods,
    verificationState: paymentVerificationState,
    executeVerification: executePaymentVerification,
    cancelVerification: cancelPaymentVerification,
    setVerificationCode: setPaymentVerificationCode,
    switchVerificationMethod: switchPaymentVerificationMethod,
    withVerification,
  } = useSecureVerification();

  const withPaymentVerification = useCallback(
    (apiCall) =>
      withVerification(apiCall, {
        preferredMethod: 'passkey',
        title: t('安全验证'),
        description: t('为了保护账户安全，请验证您的身份。'),
      }),
    [t, withVerification],
  );

  return (
    <>
      <Card style={{ marginTop: '10px' }}>
        <Tabs
          type='card'
          defaultActiveKey='overview'
          contentStyle={{ paddingTop: 24 }}
        >
          <Tabs.TabPane tab={t('支付概览')} itemKey='overview'>
            <PaymentOverviewPanel />
          </Tabs.TabPane>
          <Tabs.TabPane
            tab={t('Stripe Legacy Inventory')}
            itemKey='stripe-inventory'
          >
            <StripeLegacyInventoryPanel
              withPaymentVerification={withPaymentVerification}
            />
          </Tabs.TabPane>
        </Tabs>
      </Card>
      <SecureVerificationModal
        visible={paymentVerificationVisible}
        verificationMethods={paymentVerificationMethods}
        verificationState={paymentVerificationState}
        onVerify={(method, code) =>
          executePaymentVerification(method, code).catch(() => {
            // useSecureVerification already reports the failure to the user.
          })
        }
        onCancel={cancelPaymentVerification}
        onCodeChange={setPaymentVerificationCode}
        onMethodSwitch={switchPaymentVerificationMethod}
        title={paymentVerificationState.title}
        description={paymentVerificationState.description}
      />
    </>
  );
};

export default PaymentOperationsSetting;
