#ifndef OPENSSL_EVP_H
#define OPENSSL_EVP_H
/* Minimal stub — Bellard's encrypted vfsync uses EVP; for local
   debugging we don't load encrypted content, but fs_wget.c references
   these symbols. We supply enough types to compile. */
typedef struct evp_pkey_st EVP_PKEY;
typedef struct evp_cipher_st EVP_CIPHER;
typedef struct evp_cipher_ctx_st EVP_CIPHER_CTX;
typedef struct evp_md_st EVP_MD;
typedef struct evp_md_ctx_st EVP_MD_CTX;
#endif
