import { useEffect, useRef, useState, type ImgHTMLAttributes, type ReactNode } from "react";

import { getResourceAccess, resolveResourceAccessURL, resourceIdFromStorageKey } from "@/services/api/resources";
import { resolveImageUrl } from "@/services/image-storage";

type CachedResourceImageProps = Omit<ImgHTMLAttributes<HTMLImageElement>, "src"> & {
    storageKey?: string;
    src?: string;
    fallback?: ReactNode;
    loadingFallback?: ReactNode;
    eager?: boolean;
};

/**
 * 远程资源图片统一使用 OSS/CDN 授权地址。
 * Blob 缓存仍可用于导出、抽帧等字节处理，但不作为媒体展示 src，避免把
 * `blob:http(s)://...` 泄露到节点、素材库和浏览器媒体链路中。
 */
export function CachedResourceImage({ storageKey, src = "", fallback = null, loadingFallback = fallback, eager = false, onError, ...props }: CachedResourceImageProps) {
    const resourceId = resourceIdFromStorageKey(storageKey);
    const remoteResource = Boolean(resourceId);
    const localImageResource = Boolean(storageKey && storageKey.startsWith("image:"));
    const targetRef = useRef<HTMLSpanElement>(null);
    const [nearViewport, setNearViewport] = useState(eager || !remoteResource);
    const [cachedSrc, setCachedSrc] = useState(remoteResource ? "" : src);
    const [cacheFailed, setCacheFailed] = useState(false);
    const reSignedRef = useRef(false);

    useEffect(() => {
        if (!remoteResource || eager) {
            setNearViewport(true);
            return;
        }
        const image = targetRef.current;
        if (!image || typeof IntersectionObserver === "undefined") {
            setNearViewport(true);
            return;
        }
        const observer = new IntersectionObserver(
            (entries) => {
                if (entries.some((entry) => entry.isIntersecting)) {
                    setNearViewport(true);
                    observer.disconnect();
                }
            },
            { rootMargin: "240px" },
        );
        observer.observe(image);
        return () => observer.disconnect();
    }, [eager, remoteResource]);

    useEffect(() => {
        let cancelled = false;
        setCacheFailed(false);
        reSignedRef.current = false; // 换资源后允许重新试一次重签

        if (remoteResource && resourceId) {
            if (!nearViewport) {
                setCachedSrc("");
                return () => {
                    cancelled = true;
                };
            }
            void getResourceAccess(storageKey, "display")
                .then((access) => {
                    if (!cancelled) setCachedSrc(resolveResourceAccessURL(access.url));
                })
                .catch(() => {
                    if (!cancelled) setCacheFailed(true);
                });
            return () => {
                cancelled = true;
            };
        }

        if (localImageResource && storageKey) {
            void resolveImageUrl(storageKey, src)
                .then((url) => {
                    if (!cancelled) setCachedSrc(url || src);
                })
                .catch(() => {
                    if (!cancelled) setCachedSrc(src);
                });
            return () => {
                cancelled = true;
            };
        }

        setCachedSrc(src);
        return () => {
            cancelled = true;
        };
    }, [localImageResource, nearViewport, remoteResource, resourceId, src, storageKey]);

    const handleImgError = (e: React.SyntheticEvent<HTMLImageElement, Event>) => {
        if (localImageResource && storageKey && cachedSrc.startsWith("blob:")) {
            void resolveImageUrl(storageKey)
                .then((url) => {
                    if (url && url !== cachedSrc) {
                        setCachedSrc(url);
                        return;
                    }
                    setCacheFailed(true);
                    onError?.(e);
                })
                .catch(() => {
                    setCacheFailed(true);
                    onError?.(e);
                });
            return;
        }
        // 远程签名 URL 有 TTL，过期后同一张图会一直 403。重签一次就好：
        // 复用上面已经在用的 getResourceAccess，不引新机制。只试一次，
        // 否则真 404（资源已删）会变成无限重签的请求风暴。
        if (remoteResource && resourceId && !cacheFailed && !reSignedRef.current) {
            reSignedRef.current = true;
            setCacheFailed(false);
            void getResourceAccess(storageKey, "display")
                .then((access) => setCachedSrc(resolveResourceAccessURL(access.url)))
                .catch(() => {
                    setCacheFailed(true);
                    onError?.(e);
                });
            return;
        }
        setCacheFailed(true);
        onError?.(e);
    };

    if (!remoteResource) {
        if (cacheFailed && fallback) return <>{fallback}</>;
        return <img {...props} src={cachedSrc} onError={handleImgError} />;
    }
    return (
        <span ref={targetRef} className="cached-resource-image-shell">
            {cachedSrc && !cacheFailed ? <img {...props} src={cachedSrc} onError={handleImgError} /> : cacheFailed ? fallback : loadingFallback}
        </span>
    );
}
