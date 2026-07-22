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

import i18next from 'i18next';

import { API } from '../helpers';
import {
  prepareCredentialRequestOptions,
  buildAssertionResult,
  isPasskeySupported,
} from '../helpers/passkey';

const secureVerificationErrorKeys = {
  secure_verification_auth_required:
    'Sign in again before completing security verification.',
  secure_verification_request_invalid:
    'The security verification request is invalid. Try again.',
  secure_verification_user_unavailable:
    'Security verification is temporarily unavailable. Try again.',
  secure_verification_account_disabled:
    'This account cannot complete security verification.',
  secure_verification_method_unavailable:
    'This verification method is unavailable. Choose another method or try again.',
  secure_verification_not_configured:
    'Enable Two-factor Authentication or Passkey before continuing.',
  secure_verification_code_required:
    'Please enter the verification code or backup code.',
  secure_verification_passkey_state_invalid:
    'Passkey verification expired or is invalid. Start again.',
  secure_verification_passkey_required:
    'Complete Passkey verification before continuing.',
  secure_verification_session_unavailable:
    'Security verification could not be saved. Try again.',
  secure_verification_method_invalid:
    'This security verification method is not supported.',
  secure_verification_failed:
    'The verification code or backup code is incorrect.',
};

const secureVerificationResponseFromError = (error) => {
  const data = error?.response?.data;
  return data && typeof data === 'object' ? data : undefined;
};

const createSecureVerificationError = (response, fallbackKey, cause) => {
  const code =
    typeof response?.code === 'string' && response.code.trim()
      ? response.code.trim()
      : undefined;
  const error = new Error(
    i18next.t(secureVerificationErrorKeys[code] || fallbackKey),
  );
  error.name = 'SecureVerificationError';
  error.secureVerification = true;
  if (code) error.code = code;
  if (cause !== undefined) error.cause = cause;
  return error;
};

const createVerificationCancelledError = () => {
  const error = createSecureVerificationError(
    undefined,
    'Passkey verification was cancelled.',
  );
  error.code = 'VERIFICATION_CANCELLED';
  return error;
};

/**
 * 通用安全验证服务
 * 验证状态完全由后端 Session 控制，前端不存储任何状态
 */
export class SecureVerificationService {
  /**
   * 检查用户可用的验证方式
   * @returns {Promise<{has2FA: boolean, hasPasskey: boolean, passkeySupported: boolean}>}
   */
  static async checkAvailableVerificationMethods() {
    try {
      const [twoFAResponse, passkeyResponse, passkeySupported] =
        await Promise.all([
          API.get('/api/user/2fa/status', { skipErrorHandler: true }),
          API.get('/api/user/passkey', { skipErrorHandler: true }),
          isPasskeySupported(),
        ]);

      const has2FA =
        twoFAResponse.data?.success &&
        twoFAResponse.data?.data?.enabled === true;
      const hasPasskey =
        passkeyResponse.data?.success &&
        passkeyResponse.data?.data?.enabled === true;

      const result = {
        has2FA,
        hasPasskey,
        passkeySupported,
      };

      return result;
    } catch {
      return {
        has2FA: false,
        hasPasskey: false,
        passkeySupported: false,
      };
    }
  }

  /**
   * 执行2FA验证
   * @param {string} code - 验证码
   * @returns {Promise<void>}
   */
  static async verify2FA(code) {
    if (!code?.trim()) {
      throw createSecureVerificationError(
        undefined,
        'Please enter the verification code or backup code.',
      );
    }

    try {
      // 调用通用验证 API，验证成功后后端会设置 session
      const verifyResponse = await API.post(
        '/api/verify',
        {
          method: '2fa',
          code: code.trim(),
        },
        { skipBusinessError: true, skipErrorHandler: true },
      );

      if (!verifyResponse.data?.success) {
        throw createSecureVerificationError(
          verifyResponse.data,
          'Verification failed',
        );
      }
    } catch (error) {
      if (error?.secureVerification) throw error;
      throw createSecureVerificationError(
        secureVerificationResponseFromError(error),
        'Verification failed',
        error,
      );
    }

    // 验证成功，session 已在后端设置
  }

  /**
   * 执行Passkey验证
   * @returns {Promise<void>}
   */
  static async verifyPasskey() {
    try {
      // 开始Passkey验证
      const beginResponse = await API.post(
        '/api/user/passkey/verify/begin',
        {},
        { skipErrorHandler: true },
      );
      if (!beginResponse.data?.success) {
        throw createSecureVerificationError(
          beginResponse.data,
          'Passkey verification could not be started. Try again.',
        );
      }

      // 准备WebAuthn选项
      const publicKey = prepareCredentialRequestOptions(
        beginResponse.data.data.options,
      );

      // 执行WebAuthn验证
      const credential = await navigator.credentials.get({ publicKey });
      if (!credential) {
        throw createVerificationCancelledError();
      }

      // 构建验证结果
      const assertionResult = buildAssertionResult(credential);
      if (!assertionResult) {
        throw createSecureVerificationError(
          undefined,
          'Passkey verification could not be completed. Try again.',
        );
      }

      // 完成验证
      const finishResponse = await API.post(
        '/api/user/passkey/verify/finish',
        assertionResult,
        { skipErrorHandler: true },
      );
      if (!finishResponse.data?.success) {
        throw createSecureVerificationError(
          finishResponse.data,
          'Passkey verification failed. Try again.',
        );
      }

      // 调用通用验证 API 设置 session（Passkey 验证已完成）
      const verifyResponse = await API.post(
        '/api/verify',
        {
          method: 'passkey',
        },
        { skipErrorHandler: true },
      );

      if (!verifyResponse.data?.success) {
        throw createSecureVerificationError(
          verifyResponse.data,
          'Security verification could not be completed. Try again.',
        );
      }

      // 验证成功，session 已在后端设置
    } catch (error) {
      if (error?.secureVerification) throw error;
      if (error?.name === 'NotAllowedError') {
        throw createVerificationCancelledError();
      } else if (error?.name === 'InvalidStateError') {
        throw createSecureVerificationError(
          undefined,
          'Passkey verification is not available in the current state.',
          error,
        );
      }
      throw createSecureVerificationError(
        secureVerificationResponseFromError(error),
        'Passkey verification failed. Try again.',
        error,
      );
    }
  }

  /**
   * 通用验证方法，根据验证类型执行相应的验证流程
   * @param {string} method - 验证方式: '2fa' | 'passkey'
   * @param {string} code - 2FA验证码（当method为'2fa'时必需）
   * @returns {Promise<void>}
   */
  static async verify(method, code = '') {
    switch (method) {
      case '2fa':
        return await this.verify2FA(code);
      case 'passkey':
        return await this.verifyPasskey();
      default:
        throw createSecureVerificationError(
          undefined,
          'This security verification method is not supported.',
        );
    }
  }
}

/**
 * 预设的API调用函数工厂
 */
export const createApiCalls = {
  /**
   * 创建查看渠道密钥的API调用
   * @param {number} channelId - 渠道ID
   */
  viewChannelKey: (channelId) => async () => {
    // 新系统中，验证已通过中间件处理，直接调用 API 即可
    const response = await API.post(`/api/channel/${channelId}/key`, {});
    return response.data;
  },

  /**
   * 创建自定义API调用
   * @param {string} url - API URL
   * @param {string} method - HTTP方法，默认为 'POST'
   * @param {Object} extraData - 额外的请求数据
   */
  custom:
    (url, method = 'POST', extraData = {}) =>
    async () => {
      // 新系统中，验证已通过中间件处理
      const data = extraData;

      let response;
      switch (method.toUpperCase()) {
        case 'GET':
          response = await API.get(url, { params: data });
          break;
        case 'POST':
          response = await API.post(url, data);
          break;
        case 'PUT':
          response = await API.put(url, data);
          break;
        case 'DELETE':
          response = await API.delete(url, { data });
          break;
        default:
          throw new Error(`不支持的HTTP方法: ${method}`);
      }
      return response.data;
    },
};
