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

const normalizeHostname = (hostname) => {
  const withoutTrailingDot = hostname.toLowerCase().replace(/\.$/, '');
  if (withoutTrailingDot.startsWith('[') && withoutTrailingDot.endsWith(']')) {
    return withoutTrailingDot.slice(1, -1);
  }
  return withoutTrailingDot;
};

const getRawHostname = (origin) => {
  const match = /^[a-z][a-z0-9+.-]*:\/\/([^/?#]*)/i.exec(origin);
  if (!match || !match[1]) return null;

  const authority = match[1];
  const hostAndPort = authority.slice(authority.lastIndexOf('@') + 1);
  if (hostAndPort.startsWith('[')) {
    const closingBracket = hostAndPort.indexOf(']');
    if (closingBracket < 0) return null;
    return normalizeHostname(hostAndPort.slice(0, closingBracket + 1));
  }

  const colonIndex = hostAndPort.lastIndexOf(':');
  return normalizeHostname(
    colonIndex >= 0 ? hostAndPort.slice(0, colonIndex) : hostAndPort,
  );
};

const isLocalDevelopmentHost = (hostname) =>
  hostname === 'localhost' ||
  hostname.endsWith('.localhost') ||
  hostname === '127.0.0.1' ||
  hostname === '::1';

const parseIPv4 = (hostname) => {
  const octets = hostname.split('.');
  if (octets.length !== 4) return null;
  const parsed = octets.map((octet) => Number(octet));
  if (
    parsed.some(
      (octet, index) =>
        !/^(?:0|[1-9]\d{0,2})$/.test(octets[index]) ||
        !Number.isInteger(octet) ||
        octet < 0 ||
        octet > 255,
    )
  ) {
    return null;
  }
  return parsed;
};

const isAmbiguousIPv4Literal = (hostname) => {
  const parts = hostname.trim().split('.');
  if (parts.length === 0) return false;

  return parts.every((part) => {
    if (!part) return false;
    const hexadecimal = part.length > 2 && /^0x/i.test(part);
    const digits = hexadecimal ? part.slice(2) : part;
    return !!digits && (hexadecimal ? /^[0-9a-f]+$/i : /^\d+$/).test(digits);
  });
};

const isBlockedIPv4 = (octets) => {
  const [first, second, third, fourth] = octets;
  return (
    first === 0 ||
    first === 10 ||
    (first === 100 && second >= 64 && second <= 127) ||
    first === 127 ||
    (first === 169 && second === 254) ||
    (first === 172 && second >= 16 && second <= 31) ||
    (first === 192 && second === 0 && third === 0) ||
    (first === 192 && second === 0 && third === 2) ||
    (first === 192 && second === 168) ||
    (first === 198 && (second === 18 || second === 19)) ||
    (first === 198 && second === 51 && third === 100) ||
    (first === 203 && second === 0 && third === 113) ||
    first >= 224 ||
    (first === 255 && second === 255 && third === 255 && fourth === 255)
  );
};

const parseIPv6 = (hostname) => {
  const zoneIndex = hostname.lastIndexOf('%');
  let value = zoneIndex >= 0 ? hostname.slice(0, zoneIndex) : hostname;
  if (!value.includes(':')) return null;

  const ipv4Tail = value.match(/(?:^|:)(\d{1,3}(?:\.\d{1,3}){3})$/);
  if (ipv4Tail) {
    const ipv4 = parseIPv4(ipv4Tail[1]);
    if (!ipv4) return null;
    const high = (ipv4[0] << 8) | ipv4[1];
    const low = (ipv4[2] << 8) | ipv4[3];
    value = `${value.slice(0, -ipv4Tail[1].length)}${high.toString(16)}:${low.toString(16)}`;
  }

  const halves = value.split('::');
  if (halves.length > 2) return null;
  const left = halves[0] ? halves[0].split(':') : [];
  const right = halves.length === 2 && halves[1] ? halves[1].split(':') : [];
  if (halves.length === 1 && left.length !== 8) return null;
  if (halves.length === 2 && left.length + right.length >= 8) return null;

  const missing = halves.length === 2 ? 8 - left.length - right.length : 0;
  const groups = [
    ...left,
    ...Array.from({ length: missing }, () => '0'),
    ...right,
  ];
  if (
    groups.length !== 8 ||
    groups.some((group) => !/^[0-9a-f]{1,4}$/i.test(group))
  ) {
    return null;
  }
  return groups.map((group) => Number.parseInt(group, 16));
};

const isBlockedIPv6 = (groups) => {
  const allZero = groups.every((group) => group === 0);
  const loopback =
    groups.slice(0, 7).every((group) => group === 0) && groups[7] === 1;
  const ipv4Mapped =
    groups.slice(0, 5).every((group) => group === 0) && groups[5] === 0xffff;
  const translation =
    groups[0] === 0x64 &&
    groups[1] === 0xff9b &&
    groups.slice(2, 6).every((group) => group === 0);
  const discardOnly =
    groups[0] === 0x100 && groups.slice(1, 4).every((group) => group === 0);
  const ietfAssignments = groups[0] === 0x2001 && groups[1] <= 0x01ff;
  const documentation = groups[0] === 0x2001 && groups[1] === 0x0db8;
  const uniqueLocal = (groups[0] & 0xfe00) === 0xfc00;
  const linkLocal = (groups[0] & 0xffc0) === 0xfe80;
  const multicast = (groups[0] & 0xff00) === 0xff00;

  return (
    allZero ||
    loopback ||
    ipv4Mapped ||
    translation ||
    discardOnly ||
    ietfAssignments ||
    documentation ||
    uniqueLocal ||
    linkLocal ||
    multicast
  );
};

const isBlockedLiteralIp = (hostname) => {
  const ipv4 = parseIPv4(hostname);
  if (ipv4) return isBlockedIPv4(ipv4);
  const ipv6 = parseIPv6(hostname);
  return ipv6 ? isBlockedIPv6(ipv6) : false;
};

export const isSecurePaymentCallbackOrigin = (value) => {
  const trimmed = (value || '').trim();
  if (!trimmed || trimmed.length > 2048) return false;

  const rawHostname = getRawHostname(trimmed);
  if (
    !rawHostname ||
    (!parseIPv4(rawHostname) && isAmbiguousIPv4Literal(rawHostname))
  ) {
    return false;
  }

  try {
    const parsed = new URL(trimmed);
    const hostname = normalizeHostname(parsed.hostname);
    const hasNoPath = parsed.pathname === '' || parsed.pathname === '/';
    if (
      !hostname ||
      !hasNoPath ||
      parsed.username ||
      parsed.password ||
      parsed.search ||
      parsed.hash
    ) {
      return false;
    }

    if (parsed.protocol === 'http:') {
      return isLocalDevelopmentHost(hostname);
    }
    if (parsed.protocol !== 'https:') return false;
    if (isLocalDevelopmentHost(hostname)) return false;
    return !isBlockedLiteralIp(hostname);
  } catch {
    return false;
  }
};
