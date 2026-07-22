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

import React, {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import {
  Banner,
  Button,
  Col,
  Form,
  Radio,
  RadioGroup,
  Row,
  Select,
  Spin,
} from '@douyinfe/semi-ui';
import {
  API,
  removeTrailingSlash,
  showError,
  showSuccess,
  showWarning,
} from '../../../helpers';
import { useTranslation } from 'react-i18next';
import { BookOpen, Plus, RefreshCw } from 'lucide-react';
import {
  createPaymentAdminError,
  getPaymentAdminErrorMessage,
} from '../../../helpers/payment-admin-errors';
import RetainedCredentialEmergencyControl from './RetainedCredentialEmergencyControl';
import {
  buildWaffoPancakeSavePayload,
  getWaffoPancakePricingError,
  normalizeWaffoPancakeTestMode,
} from './waffo-pancake-settings';

const defaultInputs = {
  WaffoPancakeMerchantID: '',
  WaffoPancakePrivateKey: '',
  WaffoPancakeReturnURL: '',
  WaffoPancakeUnitPrice: 1,
  WaffoPancakeMinTopUp: 1,
  WaffoPancakeTestMode: false,
};

const emptyBinding = { storeID: '', productID: '' };

export default function SettingsPaymentGatewayWaffoPancake(props) {
  const { t } = useTranslation();
  const sectionTitle = props.hideSectionTitle
    ? undefined
    : t('Waffo Pancake 设置');
  const [loading, setLoading] = useState(false);
  const [inputs, setInputs] = useState(defaultInputs);
  const [catalog, setCatalog] = useState([]);
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [creatingPair, setCreatingPair] = useState(false);
  const [binding, setBinding] = useState(emptyBinding);
  const formApiRef = useRef(null);
  const submitInFlightRef = useRef(false);
  const catalogRequestRef = useRef(0);

  useEffect(() => {
    if (!props.options || !formApiRef.current) return;

    const currentInputs = {
      WaffoPancakeMerchantID: props.options.WaffoPancakeMerchantID || '',
      WaffoPancakePrivateKey: props.options.WaffoPancakePrivateKey || '',
      WaffoPancakeReturnURL: props.options.WaffoPancakeReturnURL || '',
      WaffoPancakeUnitPrice: Number(props.options.WaffoPancakeUnitPrice) || 1,
      WaffoPancakeMinTopUp: Number(props.options.WaffoPancakeMinTopUp) || 1,
      WaffoPancakeTestMode: normalizeWaffoPancakeTestMode(
        props.options.WaffoPancakeTestMode,
      ),
    };

    setInputs(currentInputs);
    formApiRef.current.setValues(currentInputs);
    setBinding({
      storeID: props.options.WaffoPancakeStoreID || '',
      productID: props.options.WaffoPancakeProductID || '',
    });
  }, [props.options]);

  const handleFormChange = (values) => {
    setInputs(values);
  };

  const products = useMemo(
    () =>
      catalog.find((store) => store.id === binding.storeID)?.onetimeProducts ||
      [],
    [binding.storeID, catalog],
  );

  const storeOptions = useMemo(() => {
    const options = catalog.map((store) => ({
      value: store.id,
      label: `${store.name} (${store.id})`,
    }));
    if (
      binding.storeID &&
      !options.some((option) => option.value === binding.storeID)
    ) {
      options.push({ value: binding.storeID, label: binding.storeID });
    }
    return options;
  }, [binding.storeID, catalog]);

  const productOptions = useMemo(() => {
    const options = products.map((product) => ({
      value: product.id,
      label: `${product.name} (${product.id})`,
    }));
    if (
      binding.productID &&
      !options.some((option) => option.value === binding.productID)
    ) {
      options.push({ value: binding.productID, label: binding.productID });
    }
    return options;
  }, [binding.productID, products]);

  const readCatalogCredentials = useCallback(() => {
    const merchantID = (inputs.WaffoPancakeMerchantID || '').trim();
    const privateKey = (inputs.WaffoPancakePrivateKey || '').trim();
    const savedMerchantID = (
      props.options?.WaffoPancakeMerchantID || ''
    ).trim();
    const edited = merchantID !== savedMerchantID || privateKey.length > 0;
    if (!edited) return { merchantID: '', privateKey: '' };
    return { merchantID, privateKey };
  }, [
    inputs.WaffoPancakeMerchantID,
    inputs.WaffoPancakePrivateKey,
    props.options,
  ]);

  const loadCatalog = useCallback(
    async ({ notify = false, preferredBinding, errorOwner = true } = {}) => {
      const credentials = readCatalogCredentials();
      const savedMerchantID = (
        props.options?.WaffoPancakeMerchantID || ''
      ).trim();
      if (
        (!credentials.merchantID && !savedMerchantID) ||
        (credentials.merchantID && !credentials.privateKey)
      ) {
        if (notify) {
          showError(
            t('Enter Merchant ID and API private key before verification.'),
          );
        }
        return false;
      }

      try {
        return await props.withPaymentVerification(async () => {
          const requestID = ++catalogRequestRef.current;
          setCatalogLoading(true);
          try {
            const response = await API.post(
              '/api/option/waffo-pancake/catalog',
              {
                merchant_id: credentials.merchantID,
                private_key: credentials.privateKey,
              },
              { skipErrorHandler: true },
            );
            if (requestID !== catalogRequestRef.current) return false;
            const body = response.data;
            if (
              !body?.success ||
              !body.data ||
              !Array.isArray(body.data.stores)
            ) {
              throw createPaymentAdminError(
                body,
                t('Credentials verification failed'),
              );
            }
            const stores = body.data.stores;
            setCatalog(stores);
            const preferredStoreID =
              preferredBinding?.storeID || binding.storeID || '';
            const preferredProductID =
              preferredBinding?.productID || binding.productID || '';
            const preferredStore = stores.find(
              (store) => store.id === preferredStoreID,
            );
            if (
              preferredStore?.onetimeProducts?.some(
                (product) => product.id === preferredProductID,
              )
            ) {
              setBinding({
                storeID: preferredStoreID,
                productID: preferredProductID,
              });
            } else {
              const firstStore = stores.find(
                (store) => store.onetimeProducts?.length > 0,
              );
              setBinding(
                firstStore
                  ? {
                      storeID: firstStore.id,
                      productID: firstStore.onetimeProducts[0].id,
                    }
                  : emptyBinding,
              );
            }
            if (notify) {
              showSuccess(t('Credentials verified and catalog loaded.'));
            }
            return true;
          } finally {
            if (requestID === catalogRequestRef.current) {
              setCatalogLoading(false);
            }
          }
        });
      } catch (error) {
        if (!errorOwner) throw error;
        if (notify || errorOwner) {
          showError(
            getPaymentAdminErrorMessage(
              error,
              t,
              t(
                'Credentials verification failed. Check Merchant ID and API private key.',
              ),
            ),
          );
        }
        return false;
      }
    },
    [
      binding.productID,
      binding.storeID,
      props.options,
      readCatalogCredentials,
      t,
    ],
  );

  const createPair = async () => {
    if (creatingPair || catalogLoading) return;
    const credentials = readCatalogCredentials();
    const savedMerchantID = (
      props.options?.WaffoPancakeMerchantID || ''
    ).trim();
    if (
      (!credentials.merchantID && !savedMerchantID) ||
      (credentials.merchantID && !credentials.privateKey)
    ) {
      showError(
        t('Enter Merchant ID and API private key before verification.'),
      );
      return;
    }
    const returnURL = removeTrailingSlash(
      (inputs.WaffoPancakeReturnURL || '').trim(),
    );
    if (
      !returnURL &&
      !window.confirm(
        t(
          'Payment return URL is empty. Create the product without a return redirect?',
        ),
      )
    ) {
      return;
    }

    try {
      await props.withPaymentVerification(async () => {
        setCreatingPair(true);
        try {
          const response = await API.post(
            '/api/option/waffo-pancake/pair',
            {
              merchant_id: credentials.merchantID,
              private_key: credentials.privateKey,
              return_url: returnURL,
            },
            { skipErrorHandler: true },
          );
          const body = response.data;
          if (!body?.success || !body.data?.store_id) {
            if (body?.params?.orphan_store && body.params.store_id) {
              await loadCatalog({
                preferredBinding: {
                  storeID: body.params.store_id,
                  productID: '',
                },
                errorOwner: false,
              });
            }
            throw createPaymentAdminError(body, t('Creation failed'));
          }
          await loadCatalog({
            preferredBinding: {
              storeID: body.data.store_id,
              productID: body.data.product_id,
            },
            errorOwner: false,
          });
          showSuccess(t('Store and product created.'));
          return body;
        } finally {
          setCreatingPair(false);
        }
      });
    } catch (error) {
      showError(getPaymentAdminErrorMessage(error, t, t('Creation failed')));
    }
  };

  const submitWaffoPancakeSetting = async () => {
    if (submitInFlightRef.current) return;
    const values = {
      ...inputs,
      ...(formApiRef.current?.getValues?.() || {}),
    };
    const pricingError = getWaffoPancakePricingError(
      values.WaffoPancakeUnitPrice,
      values.WaffoPancakeMinTopUp,
    );
    if (pricingError === 'unit_price') {
      showError(
        t('Enter a multiplier greater than 0 and no more than 1,000,000.'),
      );
      return;
    }
    if (pricingError === 'min_top_up') {
      showError(t('Enter a whole-dollar minimum between 1 and 10,000.'));
      return;
    }
    const merchantID = (values.WaffoPancakeMerchantID || '').trim();
    const privateKey = (values.WaffoPancakePrivateKey || '').trim();
    const returnURL = removeTrailingSlash(
      (values.WaffoPancakeReturnURL || '').trim(),
    );
    const hasConnectionConfiguration = Boolean(
      merchantID ||
      privateKey ||
      returnURL ||
      binding.storeID ||
      binding.productID,
    );
    if (hasConnectionConfiguration && !merchantID) {
      showError(t('Merchant ID is required'));
      return;
    }
    if (
      hasConnectionConfiguration &&
      (!binding.storeID || !binding.productID)
    ) {
      showError(t('Pick or create both a store and a product before saving.'));
      return;
    }

    submitInFlightRef.current = true;
    setLoading(true);
    try {
      await props.withPaymentVerification(async () => {
        const response = await API.post(
          '/api/option/waffo-pancake/save',
          buildWaffoPancakeSavePayload({
            merchantID,
            privateKey,
            returnURL,
            storeID: binding.storeID,
            productID: binding.productID,
            unitPrice: values.WaffoPancakeUnitPrice,
            minTopUp: values.WaffoPancakeMinTopUp,
            testMode: values.WaffoPancakeTestMode,
            expectedVersion: props.configVersion || 1,
          }),
          { skipErrorHandler: true },
        );
        if (!response.data?.success) {
          throw createPaymentAdminError(response.data, t('更新失败'));
        }
        const nextInputs = {
          ...values,
          WaffoPancakePrivateKey: '',
          WaffoPancakeUnitPrice: Number(values.WaffoPancakeUnitPrice),
          WaffoPancakeMinTopUp: Number(values.WaffoPancakeMinTopUp),
          WaffoPancakeTestMode: normalizeWaffoPancakeTestMode(
            values.WaffoPancakeTestMode,
          ),
        };
        setInputs(nextInputs);
        formApiRef.current?.setValues(nextInputs);
        const refreshed = await props.refresh?.(response.data?.data?.version);
        if (refreshed === false) {
          showWarning(t('设置已保存，但最新状态刷新失败'));
        } else {
          showSuccess(t('更新成功'));
        }
        return response;
      });
    } catch (error) {
      showError(getPaymentAdminErrorMessage(error, t, t('更新失败')));
    } finally {
      submitInFlightRef.current = false;
      setLoading(false);
    }
  };

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
                {t('Waffo Pancake merchant credentials are available in the')}{' '}
                <a
                  href='https://pancake.waffo.ai/merchant/dashboard'
                  target='_blank'
                  rel='noreferrer'
                >
                  {t('Waffo Pancake Console')}
                </a>{' '}
                {t(
                  'Verify the merchant credentials, then create or select the Store and Product used for wallet top-ups.',
                )}
                <br />
                {t('Test 回调地址')}：
                {props.options.ServerAddress
                  ? removeTrailingSlash(props.options.ServerAddress)
                  : t('网站地址')}
                /api/waffo-pancake/webhook/test
                <br />
                {t('Production 回调地址')}：
                {props.options.ServerAddress
                  ? removeTrailingSlash(props.options.ServerAddress)
                  : t('网站地址')}
                /api/waffo-pancake/webhook/prod
                <br />
                {t(
                  'Choose the environment that matches the Waffo Pancake merchant account, signing private key, and webhook URL. A mismatch is treated as a payment anomaly and sent to manual review.',
                )}
              </>
            }
            style={{ marginBottom: 12 }}
          />
          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.Input
                field='WaffoPancakeMerchantID'
                label={t('商户 ID')}
                placeholder={t('例如：MER_xxx')}
              />
            </Col>
            <Col xs={24} sm={24} md={12} lg={12} xl={12}>
              <Form.Input
                field='WaffoPancakeReturnURL'
                label={t('支付返回地址')}
                placeholder={t('例如：https://example.com/console/topup')}
              />
            </Col>
          </Row>

          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24}>
              <Form.TextArea
                field='WaffoPancakePrivateKey'
                label={t('API 私钥')}
                placeholder={t('填写后覆盖当前私钥，留空表示保持当前不变')}
                extraText={t(
                  'Leave this field blank to keep the saved signing private key.',
                )}
                type='password'
                autosize={{ minRows: 4, maxRows: 8 }}
              />
            </Col>
          </Row>

          <Row
            gutter={{ xs: 8, sm: 16, md: 24, lg: 24, xl: 24, xxl: 24 }}
            style={{ marginTop: 16 }}
          >
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <div className='flex h-full flex-col gap-2'>
                <span className='text-sm font-medium'>
                  {t('Payment environment')}
                </span>
                <RadioGroup
                  type='button'
                  value={inputs.WaffoPancakeTestMode ? 'test' : 'production'}
                  onChange={(event) => {
                    const testMode = event.target.value === 'test';
                    setInputs((current) => ({
                      ...current,
                      WaffoPancakeTestMode: testMode,
                    }));
                    formApiRef.current?.setValue(
                      'WaffoPancakeTestMode',
                      testMode,
                    );
                  }}
                >
                  <Radio value='production'>{t('Production')}</Radio>
                  <Radio value='test'>{t('Test')}</Radio>
                </RadioGroup>
                <span className='text-xs text-gray-500'>
                  {t(
                    'The selected environment must match the merchant account, signing private key, and the matching webhook URL shown above.',
                  )}
                </span>
              </div>
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.InputNumber
                field='WaffoPancakeUnitPrice'
                min={0.000001}
                max={1000000}
                precision={4}
                label={t('USD base price multiplier')}
                extraText={t(
                  'Positive multiplier applied to the USD base price to calculate the settlement amount for wallet top-ups and fixed-term purchases.',
                )}
              />
            </Col>
            <Col xs={24} sm={24} md={8} lg={8} xl={8}>
              <Form.InputNumber
                field='WaffoPancakeMinTopUp'
                min={1}
                max={10000}
                precision={0}
                label={t('Minimum wallet top-up (USD)')}
                extraText={t(
                  'Smallest wallet top-up amount a user can enter, measured in USD. It does not set a minimum for fixed-term purchases.',
                )}
              />
            </Col>
          </Row>

          <div className='mt-4 flex flex-wrap gap-2'>
            <Button
              theme='outline'
              icon={<RefreshCw size={16} />}
              loading={catalogLoading}
              disabled={catalogLoading || creatingPair}
              onClick={() => void loadCatalog({ notify: true })}
            >
              {t('Verify credentials and load catalog')}
            </Button>
            <Button
              theme='outline'
              icon={<Plus size={16} />}
              loading={creatingPair}
              disabled={catalogLoading || creatingPair}
              onClick={() => void createPair()}
            >
              {t('Create store and product')}
            </Button>
          </div>

          <div className='mt-4 grid gap-4 md:grid-cols-2'>
            <label className='flex flex-col gap-2 text-sm font-medium'>
              {t('Store')}
              <Select
                value={binding.storeID || undefined}
                optionList={storeOptions}
                placeholder={t('Select a store')}
                emptyContent={t('No stores are available')}
                onChange={(storeID) => {
                  const store = catalog.find((item) => item.id === storeID);
                  setBinding({
                    storeID,
                    productID: store?.onetimeProducts?.[0]?.id || '',
                  });
                }}
              />
            </label>
            <label className='flex flex-col gap-2 text-sm font-medium'>
              {t('Product')}
              <Select
                value={binding.productID || undefined}
                optionList={productOptions}
                placeholder={t('Select a product')}
                emptyContent={t('No products are available')}
                disabled={!binding.storeID}
                onChange={(productID) =>
                  setBinding((current) => ({ ...current, productID }))
                }
              />
            </label>
          </div>

          <p className='mt-3 mb-0 text-sm text-gray-500'>
            {t(
              'The selected store and product power wallet top-ups. Subscription plans keep their own product bindings.',
            )}
          </p>

          <Button
            className='mt-4'
            loading={loading}
            onClick={submitWaffoPancakeSetting}
          >
            {t('更新 Waffo Pancake 设置')}
          </Button>

          <RetainedCredentialEmergencyControl
            provider='waffo_pancake'
            disabled={loading || catalogLoading || creatingPair}
            withPaymentVerification={props.withPaymentVerification}
            onCompleted={async (result) => {
              const nextInputs = { ...inputs, WaffoPancakePrivateKey: '' };
              setInputs(nextInputs);
              formApiRef.current?.setValues(nextInputs);
              setBinding((current) => ({
                storeID: '',
                productID: current.productID,
              }));
              return (await props.refresh?.(result.data?.version)) !== false;
            }}
            onStale={() => props.refresh?.()}
          />
        </Form.Section>
      </Form>
    </Spin>
  );
}
