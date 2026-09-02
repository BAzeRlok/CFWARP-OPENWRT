include $(TOPDIR)/rules.mk

PKG_NAME:=warp-usque
PKG_VERSION:=4.2.1
PKG_RELEASE:=4

PKG_SOURCE:=usque-$(PKG_VERSION).tar.gz
PKG_SOURCE_URL:=https://codeload.github.com/Diniboy1123/usque/tar.gz/refs/tags/v$(PKG_VERSION)?
PKG_HASH:=cd0e9c89353915cb74a0877ab6fc68ee158adf8724ef9aab878a4d90edcb1d72
PKG_BUILD_DIR:=$(BUILD_DIR)/usque-$(PKG_VERSION)

PKG_LICENSE:=MIT
PKG_LICENSE_FILES:=LICENSE.md
PKG_MAINTAINER:=luci-app-warp contributors

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/Diniboy1123/usque
GO_PKG_BUILD_PKG:=$(GO_PKG)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/warp-usque
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Dedicated MASQUE backend for luci-app-warp
  URL:=https://github.com/Diniboy1123/usque
  DEPENDS:=$(GO_ARCH_DEPENDS) +ca-bundle
endef

define Package/warp-usque/description
  A private instance of usque for luci-app-warp. The Cloudflare registration
  transport uses a verified Chrome-like TLS handshake with TLS record
  fragmentation and does not depend on an external traffic-processing service.
endef

define Package/warp-usque/install
	$(INSTALL_DIR) $(1)/usr/libexec
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/usque $(1)/usr/libexec/warp-usque
endef

$(eval $(call BuildPackage,warp-usque))
