import _Image, { ImageLoaderProps, ImageProps } from "next/image";

function loader({ src, width, quality }: ImageLoaderProps) {
  return `${src}?w=${width}&q=${quality || 75}`;
}

export function image(props: ImageProps): JSX.Element {
  const _props = Object.assign({}, props, { loader: loader });

  return <_Image {..._props} />;
}

export default image;
