include $(TOPDIR)/rules.mk

PKG_NAME:=warp-warpscout
PKG_VERSION:=0.16.0
PKG_RELEASE:=1

PKG_SOURCE:=warpscout-$(PKG_VERSION).tar.gz
PKG_SOURCE_URL:=https://codeload.github.com/vernette/warpscout/tar.gz/refs/tags/v$(PKG_VERSION)?
PKG_HASH:=c21c777239856401f6529e4e2503d9f6ebd9071f78988fe86e35aaefc65a1c20
PKG_BUILD_DIR:=$(BUILD_DIR)/warpscout-$(PKG_VERSION)

PKG_LICENSE:=MIT
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=luci-app-warp contributors

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/vernette/warpscout
GO_PKG_BUILD_PKG:=$(GO_PKG)
GO_PKG_LDFLAGS_X:=main.version=$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/warp-warpscout
  SECTION:=net
  CATEGORY:=Network
  TITLE:=WARP endpoint discovery helper for luci-app-warp
  URL:=https://github.com/vernette/warpscout
  DEPENDS:=$(GO_ARCH_DEPENDS) +ca-bundle
endef

define Package/warp-warpscout/description
  A private WARPSCOUT build used to register WARP and select a verified
  AmneziaWG endpoint outside excluded exit locations.
endef

define Package/warp-warpscout/install
	$(INSTALL_DIR) $(1)/usr/libexec
	$(INSTALL_BIN) $(GO_PKG_BUILD_BIN_DIR)/warpscout $(1)/usr/libexec/warp-warpscout
endef

$(eval $(call BuildPackage,warp-warpscout))
