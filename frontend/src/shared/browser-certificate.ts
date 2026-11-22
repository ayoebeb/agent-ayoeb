// A certificate Chromium rejected on a Browser panel page, as the panel shows
// it. The fingerprint is the SHA-256 of the certificate's DER, as uppercase hex
// pairs joined by colons.
export type BrowserCertificateError = {
	host: string;
	issuer: string;
	fingerprint: string;
	error: string;
};

// One certificate the person chose to trust for one host.
export type BrowserTrustedCertificate = {
	host: string;
	fingerprint: string;
	issuer: string;
	trustedAt: string;
};
